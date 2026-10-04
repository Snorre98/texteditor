//! The render-only UI snapshot and its pure event reduction (ADR-0013 §3,
//! ADR-0046 §4).
//!
//! The UI holds no domain state: every field here is engine-sourced and rendered
//! as-is. [`AppState::reduce`] is a pure function over [`UiEvent`] so the state
//! machine (token accumulation, done/degraded, candidate/conflict labels) is
//! unit-tested without a terminal or a live engine.

use crate::gen::{DiffEvent, MeterEvent, Mode, Revision};
use crate::sse::SseEvent;

/// Connection lifecycle for the status line.
#[derive(Debug, Clone, PartialEq, Eq, Default)]
pub enum ConnectionState {
    #[default]
    Connecting,
    Connected {
        base_url: String,
    },
    Unreachable {
        error: String,
    },
}

/// Who authored a chat line (display only).
#[derive(Debug, Clone, Copy, PartialEq, Eq, Default)]
pub enum Role {
    #[default]
    User,
    Assistant,
    System,
}

/// One chat line.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ChatMessage {
    pub role: Role,
    pub text: String,
}

/// The staged-candidate preview for the diff pane.
#[derive(Debug, Clone, PartialEq, Eq, Default)]
pub struct CandidatePreview {
    pub block_id: String,
    pub error: Option<String>,
}

/// Engine-sourced status-line fields.
#[derive(Debug, Clone, Default)]
pub struct StatusInfo {
    pub used_model: Option<String>,
    pub degraded: bool,
    pub written_through: bool,
    pub target_path: Option<String>,
    /// A labeled conflict (e.g. `file-changed-externally`), cleared on approve.
    pub conflict: Option<String>,
}

/// An event from the async bridge to the UI. The UI reduces these into
/// [`AppState`]; it never computes domain values.
#[derive(Debug, Clone)]
#[allow(clippy::large_enum_variant)]
pub enum UiEvent {
    /// Discovery succeeded; the base URL the client adopted.
    Discovered { base_url: String },
    /// Discovery/probe failed.
    ConnectFailed(String),
    /// Preset tabs (`GET /modes`).
    Modes(Vec<Mode>),
    /// The opened document.
    Document {
        id: String,
        path: String,
        external_change: bool,
    },
    /// The resumed/created session.
    Session { id: String },
    /// One typed SSE event.
    Turn(SseEvent),
    /// The turn stream ended (terminal event or disconnect).
    TurnEnded,
    /// A labeled protocol note (unknown/invalid event, backpressure).
    ProtocolNote(String),
    /// Approve succeeded and write-through was attempted.
    Approved { revision: Revision },
    /// Approve hit an external-change conflict (409).
    Conflict { message: String },
    /// A non-fatal error to surface.
    Error(String),
    /// An informational note.
    Info(String),
}

/// The full render-only snapshot.
#[derive(Debug, Default)]
pub struct AppState {
    pub connection: ConnectionState,
    pub modes: Vec<Mode>,
    pub active_mode: Option<String>,
    pub document_id: Option<String>,
    pub document_path: Option<String>,
    pub session_id: Option<String>,
    /// Finalized chat history.
    pub messages: Vec<ChatMessage>,
    /// The assistant text currently streaming (folded into `messages` on end).
    pub streaming: Option<String>,
    pub meter: Option<MeterEvent>,
    pub candidate: Option<CandidatePreview>,
    pub diffs: Vec<DiffEvent>,
    pub status: StatusInfo,
    /// The input line buffer (owned by the UI thread).
    pub input: String,
    pub turn_active: bool,
    /// Recent protocol labels (unknown/invalid events), newest last, capped.
    pub protocol_notes: Vec<String>,
    pub error: Option<String>,
    pub should_quit: bool,
}

impl AppState {
    const MAX_NOTES: usize = 20;

    /// Begin a turn: append the user line, clear the stream buffer and stale
    /// status, and mark the turn active.
    pub fn begin_turn(&mut self, input: String) {
        let text = input.trim().to_string();
        if text.is_empty() {
            return;
        }
        self.messages.push(ChatMessage {
            role: Role::User,
            text,
        });
        self.streaming = Some(String::new());
        self.turn_active = true;
        self.status.conflict = None;
        self.error = None;
        self.candidate = None;
        self.diffs.clear();
        self.input.clear();
    }

    fn finalize_streaming(&mut self) {
        if let Some(text) = self.streaming.take() {
            if !text.is_empty() {
                self.messages.push(ChatMessage {
                    role: Role::Assistant,
                    text,
                });
            }
        }
    }

    fn note(&mut self, note: String) {
        self.protocol_notes.push(note);
        if self.protocol_notes.len() > Self::MAX_NOTES {
            let overflow = self.protocol_notes.len() - Self::MAX_NOTES;
            self.protocol_notes.drain(..overflow);
        }
    }

    /// The index of the active preset in `modes` (0 when none/unknown).
    pub fn active_mode_index(&self) -> usize {
        self.active_mode
            .as_ref()
            .and_then(|name| self.modes.iter().position(|m| &m.name == name))
            .unwrap_or(0)
    }

    /// Apply one bridge event to the snapshot.
    pub fn reduce(&mut self, event: UiEvent) {
        match event {
            UiEvent::Discovered { base_url } => {
                self.connection = ConnectionState::Connected { base_url };
            }
            UiEvent::ConnectFailed(error) => {
                self.connection = ConnectionState::Unreachable { error };
            }
            UiEvent::Modes(modes) => {
                if self.active_mode.is_none() {
                    self.active_mode = modes.first().map(|m| m.name.clone());
                }
                self.modes = modes;
            }
            UiEvent::Document {
                id,
                path,
                external_change,
            } => {
                self.status.target_path = Some(path.clone());
                self.document_id = Some(id);
                self.document_path = Some(path);
                if external_change {
                    self.note("external-change: file re-read from disk on open".to_string());
                }
            }
            UiEvent::Session { id } => {
                self.session_id = Some(id);
            }
            UiEvent::Turn(SseEvent::Token(token)) => {
                self.streaming
                    .get_or_insert_with(String::new)
                    .push_str(&token.text);
            }
            UiEvent::Turn(SseEvent::Meter(meter)) => {
                self.meter = Some(meter);
            }
            UiEvent::Turn(SseEvent::Candidate(candidate)) => {
                self.candidate = Some(CandidatePreview {
                    block_id: candidate.block_id.clone().unwrap_or_default(),
                    error: candidate.error.map(|e| e.as_str().to_string()),
                });
                if let Some(rev) = candidate.revision {
                    if let Some(model) = rev.message {
                        self.note(format!("candidate staged: {model}"));
                    }
                }
            }
            UiEvent::Turn(SseEvent::Diff(diff)) => {
                self.diffs.push(diff);
            }
            UiEvent::Turn(SseEvent::Rag(_)) | UiEvent::Turn(SseEvent::Context(_)) => {
                // E1 decodes but does not render RAG/context; E2 owns those panes.
            }
            UiEvent::Turn(SseEvent::Done(done)) => {
                self.status.used_model = done.used_model;
                self.status.degraded = done.degraded.unwrap_or(false);
                self.finalize_streaming();
                self.turn_active = false;
            }
            UiEvent::Turn(SseEvent::Error(error)) => {
                let code = error.code.unwrap_or_else(|| "error".to_string());
                let message = error.message.unwrap_or_default();
                self.error = Some(format!("{code}: {message}"));
                self.finalize_streaming();
                self.turn_active = false;
            }
            UiEvent::Turn(SseEvent::Backpressure(_)) => {
                self.note("backpressure: events dropped by the engine".to_string());
            }
            UiEvent::TurnEnded => {
                self.finalize_streaming();
                self.turn_active = false;
            }
            UiEvent::ProtocolNote(note) => self.note(note),
            UiEvent::Approved { revision } => {
                self.status.written_through = revision.written_through.unwrap_or(false);
                if let Some(path) = revision.path {
                    self.status.target_path = Some(path);
                }
                self.status.conflict = None;
                self.candidate = None;
                self.diffs.clear();
            }
            UiEvent::Conflict { message } => {
                self.status.conflict = Some(message);
            }
            UiEvent::Error(error) => {
                self.error = Some(error);
                self.turn_active = false;
            }
            UiEvent::Info(note) => self.note(note),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::gen::{CandidateEvent, DoneEvent, TokenEvent};

    fn token(text: &str) -> UiEvent {
        UiEvent::Turn(SseEvent::Token(TokenEvent {
            text: text.to_string(),
        }))
    }

    #[test]
    fn tokens_accumulate_into_the_streaming_buffer() {
        let mut app = AppState::default();
        app.begin_turn("hello".to_string());
        app.reduce(token("Hel"));
        app.reduce(token("lo"));
        assert_eq!(app.streaming.as_deref(), Some("Hello"));
        assert!(app.turn_active);
    }

    #[test]
    fn done_finalizes_stream_and_records_model() {
        let mut app = AppState::default();
        app.begin_turn("hi".to_string());
        app.reduce(token("Hi there"));
        app.reduce(UiEvent::Turn(SseEvent::Done(DoneEvent {
            degraded: Some(true),
            used_model: Some("gemma".to_string()),
        })));
        assert!(!app.turn_active);
        assert_eq!(app.streaming, None);
        assert_eq!(app.messages.last().unwrap().text, "Hi there");
        assert_eq!(app.messages.last().unwrap().role, Role::Assistant);
        assert_eq!(app.status.used_model.as_deref(), Some("gemma"));
        assert!(app.status.degraded);
    }

    #[test]
    fn candidate_sets_preview_and_error_label() {
        let mut app = AppState::default();
        app.reduce(UiEvent::Turn(SseEvent::Candidate(CandidateEvent {
            ok: true,
            block_id: Some("b1".to_string()),
            error: None,
            issues: None,
            revision: None,
        })));
        assert_eq!(app.candidate.as_ref().unwrap().block_id, "b1");
        assert!(app.candidate.as_ref().unwrap().error.is_none());
    }

    #[test]
    fn failed_candidate_records_the_error_label() {
        let mut app = AppState::default();
        app.reduce(UiEvent::Turn(SseEvent::Candidate(CandidateEvent {
            ok: false,
            block_id: Some("b1".to_string()),
            error: Some(crate::gen::CandidateEventError::GuardFailed),
            issues: None,
            revision: None,
        })));
        assert_eq!(
            app.candidate.as_ref().unwrap().error.as_deref(),
            Some("guard-failed")
        );
    }

    #[test]
    fn approved_records_write_through_and_clears_candidate() {
        let mut app = AppState::default();
        app.reduce(UiEvent::Turn(SseEvent::Candidate(CandidateEvent {
            ok: true,
            block_id: Some("b1".to_string()),
            error: None,
            issues: None,
            revision: None,
        })));
        app.reduce(UiEvent::Approved {
            revision: Revision {
                written_through: Some(true),
                path: Some("/vault/note.md".to_string()),
                ..Default::default()
            },
        });
        assert!(app.status.written_through);
        assert_eq!(app.status.target_path.as_deref(), Some("/vault/note.md"));
        assert!(app.candidate.is_none());
        assert!(app.status.conflict.is_none());
    }

    #[test]
    fn conflict_sets_the_label() {
        let mut app = AppState::default();
        app.reduce(UiEvent::Conflict {
            message: "file-changed-externally: /vault/note.md".to_string(),
        });
        assert_eq!(
            app.status.conflict.as_deref(),
            Some("file-changed-externally: /vault/note.md")
        );
    }

    #[test]
    fn protocol_notes_are_recorded_and_capped() {
        let mut app = AppState::default();
        for i in 0..(AppState::MAX_NOTES + 5) {
            app.reduce(UiEvent::ProtocolNote(format!("note-{i}")));
        }
        assert_eq!(app.protocol_notes.len(), AppState::MAX_NOTES);
        assert_eq!(app.protocol_notes.last().unwrap(), "note-24");
    }

    #[test]
    fn error_event_ends_the_turn_with_a_label() {
        let mut app = AppState::default();
        app.begin_turn("hi".to_string());
        app.reduce(UiEvent::Turn(SseEvent::Error(crate::gen::ErrorEvent {
            code: Some("context-window-exceeded".to_string()),
            message: Some("too big".to_string()),
        })));
        assert!(!app.turn_active);
        assert_eq!(
            app.error.as_deref(),
            Some("context-window-exceeded: too big")
        );
    }
}
