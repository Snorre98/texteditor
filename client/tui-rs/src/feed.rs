//! The `/events` non-turn liveness feed decoder (ADR-0052 §4, ADR-0031).
//!
//! It reuses the byte framing of [`crate::sse::FrameDecoder`] and dispatches
//! each framed message by its SSE event name to the generated feed payload
//! type (`corpus`, `document`, `session`, `fleet`). An unknown or invalid event
//! is labeled and skipped, never fatal — the feed is long-lived and forward
//! compatible.

use crate::gen::{CorpusEvent, DocumentEvent, FleetEvent, SessionEvent};
use crate::sse::{FrameDecoder, RawMessage};

/// The non-turn feed event vocabulary, in dispatch order.
pub const FEED_EVENT_NAMES: &[&str] = &["corpus", "document", "session", "fleet"];

/// A typed feed payload, keyed by the SSE `event:` name.
#[derive(Debug, Clone)]
pub enum FeedEvent {
    Corpus(CorpusEvent),
    Document(DocumentEvent),
    Session(SessionEvent),
    Fleet(FleetEvent),
}

/// The outcome of decoding one framed feed block.
#[derive(Debug, Clone)]
pub enum FeedDecoded {
    Event(FeedEvent),
    Unknown { name: String },
    Invalid { name: String, error: String },
}

/// Dispatch a parsed message by event name to the generated payload type.
pub fn dispatch_feed(message: &RawMessage) -> FeedDecoded {
    match message.event.as_str() {
        "corpus" => from_json(message, FeedEvent::Corpus),
        "document" => from_json(message, FeedEvent::Document),
        "session" => from_json(message, FeedEvent::Session),
        "fleet" => from_json(message, FeedEvent::Fleet),
        other => FeedDecoded::Unknown {
            name: other.to_string(),
        },
    }
}

fn from_json<T>(message: &RawMessage, wrap: impl FnOnce(T) -> FeedEvent) -> FeedDecoded
where
    T: serde::de::DeserializeOwned,
{
    match serde_json::from_str::<T>(&message.data) {
        Ok(payload) => FeedDecoded::Event(wrap(payload)),
        Err(err) => FeedDecoded::Invalid {
            name: message.event.clone(),
            error: err.to_string(),
        },
    }
}

/// A streaming decoder over the `/events` byte stream.
#[derive(Debug, Default)]
pub struct FeedDecoder {
    frames: FrameDecoder,
}

impl FeedDecoder {
    pub fn new() -> Self {
        Self::default()
    }

    /// Feed one byte chunk; returns every complete block's decode outcome.
    pub fn push(&mut self, chunk: &[u8]) -> Vec<FeedDecoded> {
        self.frames
            .push(chunk)
            .iter()
            .map(dispatch_feed)
            .collect()
    }

    /// Flush any trailing partial block.
    pub fn finish(&mut self) -> Option<FeedDecoded> {
        self.frames.finish().as_ref().map(dispatch_feed)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn dispatches_each_feed_event() {
        let corpus = "event: corpus\ndata: {\"workspaceId\":\"w1\",\"job\":{\"id\":\"j1\",\"workspaceId\":\"w1\",\"kind\":\"index\",\"state\":\"running\",\"total\":2,\"completed\":1}}";
        let document = "event: document\ndata: {\"kind\":\"commit\",\"documentId\":\"d1\",\"path\":\"/v/a.md\"}";
        let session = "event: session\ndata: {\"kind\":\"created\",\"workspaceId\":\"w1\",\"session\":{\"id\":\"s1\",\"documentId\":\"d1\"}}";
        let fleet = "event: fleet\ndata: {\"control\":\"up\",\"models\":[]}";

        for (name, block) in [
            ("corpus", corpus),
            ("document", document),
            ("session", session),
            ("fleet", fleet),
        ] {
            match dispatch_feed(&crate::sse::parse_message(block).unwrap()) {
                FeedDecoded::Event(ev) => {
                    let got = match ev {
                        FeedEvent::Corpus(_) => "corpus",
                        FeedEvent::Document(_) => "document",
                        FeedEvent::Session(_) => "session",
                        FeedEvent::Fleet(_) => "fleet",
                    };
                    assert_eq!(got, name);
                }
                other => panic!("{name}: unexpected {other:?}"),
            }
        }
    }

    #[test]
    fn unknown_feed_event_is_labeled() {
        let msg = crate::sse::parse_message("event: frobnicate\ndata: {}").unwrap();
        assert!(matches!(
            dispatch_feed(&msg),
            FeedDecoded::Unknown { name } if name == "frobnicate"
        ));
    }

    #[test]
    fn invalid_feed_payload_is_labeled() {
        let msg = crate::sse::parse_message("event: document\ndata: {\"nope\":1}").unwrap();
        assert!(matches!(dispatch_feed(&msg), FeedDecoded::Invalid { .. }));
    }

    #[test]
    fn stream_decoder_splits_chunks() {
        let mut dec = FeedDecoder::new();
        assert!(dec.push(b"event: fleet\ndata: {\"control\":\"up\",\"models\":[").is_empty());
        let out = dec.push(b"]}\n\n");
        assert_eq!(out.len(), 1);
        assert!(matches!(&out[0], FeedDecoded::Event(FeedEvent::Fleet(_))));
    }
}
