//! Integration test for the public SSE decoder surface (ADR-0031, ADR-0046 §3).
//!
//! Feeds a realistic `/turn` stream through [`SseDecoder`] one byte at a time to
//! exercise partial-block and partial-UTF-8 buffering, typed dispatch, the
//! unknown-event label, and terminal detection. E2 adds `turn`, `locate`, and
//! `thinking` to the vocabulary; a genuinely unknown event is still labeled.

use texteditor_tui_rs::sse::{Decoded, SseDecoder, SseEvent};

#[test]
fn decodes_a_full_turn_stream_in_arbitrary_chunks() {
    let stream = concat!(
        "event: turn\ndata: {\"turnId\":\"t1\",\"sessionId\":\"s1\"}\n\n",
        "event: token\ndata: {\"text\":\"Hel\"}\n\n",
        "event: token\ndata: {\"text\":\"lo — 世界\"}\n\n",
        "event: meter\ndata: {\"system\":1,\"tools\":0,\"rag\":0,\"history\":0,\"mentions\":0,\"user\":1,\"thinking\":0,\"completion\":2}\n\n",
        "event: thinking\ndata: {\"text\":\"reasoning...\"}\n\n",
        "event: frobnicate\ndata: {\"x\":1}\n\n",
        "event: done\ndata: {\"usedModel\":\"gemma\",\"degraded\":false}\n\n",
    );

    let mut decoder = SseDecoder::new();
    let mut decoded = Vec::new();
    for byte in stream.as_bytes() {
        decoded.extend(decoder.push(&[*byte]));
    }

    assert_eq!(decoded.len(), 7, "one outcome per framed block");

    let mut text = String::new();
    let mut reasoning = String::new();
    let mut turn_id = None;
    let mut used_model = None;
    let mut saw_unknown = false;
    let mut terminated = false;
    for outcome in decoded {
        match outcome {
            Decoded::Event(SseEvent::Turn(turn)) => turn_id = Some(turn.turn_id),
            Decoded::Event(SseEvent::Token(token)) => text.push_str(&token.text),
            Decoded::Event(SseEvent::Meter(meter)) => assert_eq!(meter.completion, 2),
            Decoded::Event(SseEvent::Thinking(thinking)) => reasoning.push_str(&thinking.text),
            Decoded::Event(SseEvent::Done(done)) => {
                used_model = done.used_model;
                terminated = true;
            }
            Decoded::Unknown { name } => {
                assert_eq!(name, "frobnicate");
                saw_unknown = true;
            }
            other => panic!("unexpected outcome: {other:?}"),
        }
    }

    assert_eq!(turn_id.as_deref(), Some("t1"));
    assert_eq!(text, "Hello — 世界");
    assert_eq!(reasoning, "reasoning...");
    assert_eq!(used_model.as_deref(), Some("gemma"));
    assert!(saw_unknown, "an unknown event must be labeled, not decoded");
    assert!(terminated, "done must terminate the turn");
}
