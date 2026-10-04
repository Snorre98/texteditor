//! The `/turn` SSE decoder (ADR-0031 §4, ADR-0046 §3).
//!
//! The server hand-frames each message as:
//!
//! ```text
//! event: <type>\n
//! data: <json payload>\n
//! \n
//! ```
//!
//! This module mirrors the OpenTUI client's `api/sse.ts`:
//! - split the byte stream on `\n\n`;
//! - parse `event:` plus multi-line `data:` (lines joined with `\n`);
//! - skip `:` comment lines and empty/comment-only blocks;
//! - dispatch by the SSE event name to the generated payload type;
//! - **label** an unknown or invalid event and continue (never crash);
//! - stop at the terminal events `done` / `error`.
//!
//! The dispatch name set is an explicit allowlist — the current vocabulary
//! (`token, meter, candidate, diff, rag, context, done, error, backpressure`).
//! Future (`locate`, `thinking`) or unknown events are therefore labeled and
//! skipped rather than deserialized, which is what makes the decoder forward
//! compatible (ADR-0046 §3).

use crate::gen::{
    BackpressureEvent, CandidateEvent, ContextEvent, DiffEvent, DoneEvent, ErrorEvent, MeterEvent,
    RagEvent, TokenEvent,
};

/// The current SSE event vocabulary, in dispatch order.
pub const KNOWN_EVENT_NAMES: &[&str] = &[
    "token",
    "meter",
    "candidate",
    "diff",
    "rag",
    "context",
    "done",
    "error",
    "backpressure",
];

/// A typed SSE payload. Each variant wraps the generated payload type keyed by
/// the SSE `event:` name. The `context` snapshot is the large variant; boxing it
/// would only add an allocation per streamed event.
#[derive(Debug, Clone)]
#[allow(clippy::large_enum_variant)]
pub enum SseEvent {
    Token(TokenEvent),
    Meter(MeterEvent),
    Candidate(CandidateEvent),
    Diff(DiffEvent),
    Rag(RagEvent),
    Context(ContextEvent),
    Done(DoneEvent),
    Error(ErrorEvent),
    Backpressure(BackpressureEvent),
}

impl SseEvent {
    /// The wire event name for this payload.
    pub fn name(&self) -> &'static str {
        match self {
            Self::Token(_) => "token",
            Self::Meter(_) => "meter",
            Self::Candidate(_) => "candidate",
            Self::Diff(_) => "diff",
            Self::Rag(_) => "rag",
            Self::Context(_) => "context",
            Self::Done(_) => "done",
            Self::Error(_) => "error",
            Self::Backpressure(_) => "backpressure",
        }
    }

    /// `done` and `error` end the stream; `backpressure` does not (ADR-0031).
    pub fn is_terminal(&self) -> bool {
        matches!(self, Self::Done(_) | Self::Error(_))
    }
}

/// The outcome of decoding one framed block. `Unknown`/`Invalid` are labeled
/// and the stream continues.
#[derive(Debug, Clone)]
#[allow(clippy::large_enum_variant)]
pub enum Decoded {
    Event(SseEvent),
    Unknown { name: String },
    Invalid { name: String, error: String },
}

/// One parsed SSE message: a non-empty `event` name plus its joined `data`.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct RawMessage {
    pub event: String,
    pub data: String,
}

/// Parse one `event:`/`data:` block. Returns `None` for an empty or
/// comment-only block (or a block with no `event:` line).
pub fn parse_message(block: &str) -> Option<RawMessage> {
    let mut event = String::new();
    let mut data_lines: Vec<&str> = Vec::new();
    for raw_line in block.split('\n') {
        let line = raw_line.strip_suffix('\r').unwrap_or(raw_line);
        if line.starts_with(':') {
            continue;
        }
        if let Some(rest) = line.strip_prefix("event:") {
            event = rest.trim().to_string();
        } else if let Some(rest) = line.strip_prefix("data:") {
            let value = rest.strip_prefix(' ').unwrap_or(rest);
            data_lines.push(value);
        }
    }
    if event.is_empty() {
        return None;
    }
    let data = if data_lines.is_empty() {
        "{}".to_string()
    } else {
        data_lines.join("\n")
    };
    Some(RawMessage { event, data })
}

/// Dispatch a parsed message by event name to the generated payload type.
pub fn dispatch(message: &RawMessage) -> Decoded {
    match message.event.as_str() {
        "token" => from_json(message, SseEvent::Token),
        "meter" => from_json(message, SseEvent::Meter),
        "candidate" => from_json(message, SseEvent::Candidate),
        "diff" => from_json(message, SseEvent::Diff),
        "rag" => from_json(message, SseEvent::Rag),
        "context" => from_json(message, SseEvent::Context),
        "done" => from_json(message, SseEvent::Done),
        "error" => from_json(message, SseEvent::Error),
        "backpressure" => from_json(message, SseEvent::Backpressure),
        other => Decoded::Unknown {
            name: other.to_string(),
        },
    }
}

fn from_json<T>(message: &RawMessage, wrap: impl FnOnce(T) -> SseEvent) -> Decoded
where
    T: serde::de::DeserializeOwned,
{
    match serde_json::from_str::<T>(&message.data) {
        Ok(payload) => Decoded::Event(wrap(payload)),
        Err(err) => Decoded::Invalid {
            name: message.event.clone(),
            error: err.to_string(),
        },
    }
}

/// Parse + dispatch one framed block.
pub fn decode_block(block: &str) -> Option<Decoded> {
    parse_message(block).as_ref().map(dispatch)
}

/// A streaming decoder over byte chunks. Bytes are buffered until a `\n\n`
/// separator is seen; the final partial block is flushed with [`SseDecoder::finish`].
///
/// The buffer stays as **bytes**: the `\n\n` separator is ASCII, so it can never
/// occur inside a multi-byte UTF-8 sequence, and a message block before it is
/// therefore always complete UTF-8. This makes chunk boundaries that split a
/// multi-byte character safe (a per-chunk `from_utf8_lossy` would corrupt it).
#[derive(Debug, Default)]
pub struct SseDecoder {
    buffer: Vec<u8>,
}

impl SseDecoder {
    pub fn new() -> Self {
        Self::default()
    }

    /// Feed one byte chunk; returns every complete block's decode outcome.
    pub fn push(&mut self, chunk: &[u8]) -> Vec<Decoded> {
        // Drop raw CR as we buffer so a CRLF stream frames the same as LF (raw
        // CR never appears inside a JSON payload — control characters are
        // escaped by the encoder).
        self.buffer
            .extend(chunk.iter().copied().filter(|byte| *byte != b'\r'));
        let mut out = Vec::new();
        while let Some(sep) = find_subslice(&self.buffer, b"\n\n") {
            let block_bytes: Vec<u8> = self.buffer[..sep].to_vec();
            self.buffer.drain(..sep + 2);
            let block = String::from_utf8_lossy(&block_bytes);
            if let Some(decoded) = decode_block(&block) {
                out.push(decoded);
            }
        }
        out
    }

    /// Flush any trailing partial block (a stream that ended without a final
    /// blank line).
    pub fn finish(&mut self) -> Option<Decoded> {
        let tail = std::mem::take(&mut self.buffer);
        if tail.iter().all(u8::is_ascii_whitespace) {
            None
        } else {
            decode_block(&String::from_utf8_lossy(&tail))
        }
    }
}

fn find_subslice(haystack: &[u8], needle: &[u8]) -> Option<usize> {
    if needle.is_empty() || haystack.len() < needle.len() {
        return None;
    }
    haystack
        .windows(needle.len())
        .position(|window| window == needle)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parses_single_event() {
        let msg = parse_message("event: token\ndata: {\"text\":\"hi\"}").unwrap();
        assert_eq!(msg.event, "token");
        assert_eq!(msg.data, "{\"text\":\"hi\"}");
    }

    #[test]
    fn joins_multi_line_data() {
        let msg = parse_message("event: error\ndata: {\"code\":\"x\",\ndata: \"message\":\"y\"}")
            .unwrap();
        assert_eq!(msg.data, "{\"code\":\"x\",\n\"message\":\"y\"}");
    }

    #[test]
    fn skips_comments_but_keeps_event() {
        let msg = parse_message(": keep-alive\nevent: token\ndata: {\"text\":\"x\"}").unwrap();
        assert_eq!(msg.event, "token");
    }

    #[test]
    fn comment_only_block_is_none() {
        assert!(parse_message(": keep-alive\n: still alive").is_none());
    }

    #[test]
    fn missing_event_is_none() {
        assert!(parse_message("data: {}").is_none());
    }

    #[test]
    fn missing_data_defaults_to_empty_object() {
        let msg = parse_message("event: backpressure").unwrap();
        assert_eq!(msg.data, "{}");
    }

    #[test]
    fn dispatches_token_payload() {
        let decoded = decode_block("event: token\ndata: {\"text\":\"hello\"}").unwrap();
        match decoded {
            Decoded::Event(SseEvent::Token(t)) => assert_eq!(t.text, "hello"),
            other => panic!("unexpected {other:?}"),
        }
    }

    #[test]
    fn dispatches_each_known_event() {
        // A smoke payload per event proving the allowlist maps to the right variant.
        let cases: &[(&str, &str, &str)] = &[
            ("token", r#"{"text":"t"}"#, "token"),
            (
                "meter",
                r#"{"system":1,"tools":0,"rag":0,"history":0,"mentions":0,"user":1,"thinking":0,"completion":2}"#,
                "meter",
            ),
            ("candidate", r#"{"ok":true}"#, "candidate"),
            ("diff", r#"{"ok":true}"#, "diff"),
            ("rag", r#"{"ok":true}"#, "rag"),
            ("done", r#"{"usedModel":"m"}"#, "done"),
            ("error", r#"{"code":"boom"}"#, "error"),
            ("backpressure", r#"{}"#, "backpressure"),
        ];
        for (name, data, want) in cases {
            let decoded = decode_block(&format!("event: {name}\ndata: {data}")).unwrap();
            match decoded {
                Decoded::Event(ev) => assert_eq!(ev.name(), *want),
                other => panic!("{name}: unexpected {other:?}"),
            }
        }
    }

    #[test]
    fn unknown_event_is_labeled_and_not_an_error() {
        let decoded = decode_block("event: thinking\ndata: {\"text\":\"...\"}").unwrap();
        assert!(matches!(
            decoded,
            Decoded::Unknown { name } if name == "thinking"
        ));
        // locate is likewise unknown until E2.
        let decoded = decode_block("event: locate\ndata: {\"status\":\"resolved\"}").unwrap();
        assert!(matches!(decoded, Decoded::Unknown { .. }));
    }

    #[test]
    fn invalid_json_is_labeled_and_does_not_panic() {
        let decoded = decode_block("event: token\ndata: {not json}").unwrap();
        match decoded {
            Decoded::Invalid { name, .. } => assert_eq!(name, "token"),
            other => panic!("unexpected {other:?}"),
        }
    }

    #[test]
    fn schema_mismatch_is_labeled_invalid() {
        // token requires `text`; a wrong shape is an invalid labeled event.
        let decoded = decode_block("event: token\ndata: {\"nope\":1}").unwrap();
        assert!(matches!(decoded, Decoded::Invalid { .. }));
    }

    #[test]
    fn terminal_events_are_flagged() {
        let done = decode_block("event: done\ndata: {}").unwrap();
        match done {
            Decoded::Event(ev) => assert!(ev.is_terminal()),
            other => panic!("unexpected {other:?}"),
        }
        let backpressure = decode_block("event: backpressure\ndata: {}").unwrap();
        match backpressure {
            Decoded::Event(ev) => assert!(!ev.is_terminal()),
            other => panic!("unexpected {other:?}"),
        }
    }

    #[test]
    fn stream_decoder_handles_split_chunks() {
        let mut dec = SseDecoder::new();
        assert!(dec.push(b"event: tok").is_empty());
        let out = dec.push(b"en\ndata: {\"text\":\"a\"}\n\n");
        assert_eq!(out.len(), 1);
        match &out[0] {
            Decoded::Event(SseEvent::Token(t)) => assert_eq!(t.text, "a"),
            other => panic!("unexpected {other:?}"),
        }
        assert!(dec.finish().is_none());
    }

    #[test]
    fn stream_decoder_yields_multiple_blocks_in_one_chunk() {
        let mut dec = SseDecoder::new();
        let out = dec.push(b"event: token\ndata: {\"text\":\"a\"}\n\nevent: done\ndata: {}\n\n");
        assert_eq!(out.len(), 2);
        assert!(matches!(&out[1], Decoded::Event(ev) if ev.is_terminal()));
    }

    #[test]
    fn stream_decoder_flushes_trailing_partial_block() {
        let mut dec = SseDecoder::new();
        assert!(dec.push(b"event: done\ndata: {}").is_empty());
        match dec.finish() {
            Some(Decoded::Event(ev)) => assert!(ev.is_terminal()),
            other => panic!("unexpected {other:?}"),
        }
        assert!(dec.finish().is_none());
    }

    #[test]
    fn stream_decoder_tolerates_crlf() {
        let mut dec = SseDecoder::new();
        let out = dec.push(b"event: token\r\ndata: {\"text\":\"x\"}\r\n\r\n");
        assert_eq!(out.len(), 1);
    }

    #[test]
    fn stream_decoder_survives_multibyte_split_across_chunks() {
        // A one-byte-at-a-time feed splits multi-byte UTF-8 sequences; the byte
        // buffer must reassemble them (a per-chunk lossy decode would corrupt).
        let payload = "event: token\ndata: {\"text\":\"héllo — 世界\"}\n\n";
        let mut dec = SseDecoder::new();
        let mut collected = Vec::new();
        for byte in payload.as_bytes() {
            collected.extend(dec.push(&[*byte]));
        }
        assert_eq!(collected.len(), 1);
        match &collected[0] {
            Decoded::Event(SseEvent::Token(t)) => assert_eq!(t.text, "héllo — 世界"),
            other => panic!("unexpected {other:?}"),
        }
    }
}
