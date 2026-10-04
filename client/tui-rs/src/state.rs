//! The render-only UI snapshot and its pure event reduction (ADR-0013 §3,
//! ADR-0046 §4).
//!
//! The UI holds no domain state: every field here is engine-sourced and rendered
//! as-is. [`AppState::reduce`] is a pure function over [`UiEvent`] so the state
//! machine (token accumulation, done/degraded, candidate/conflict labels) is
//! unit-tested without a terminal or a live engine.

use crate::gen::{
    ContextSnapshot, CorpusDocument, CorpusJob, CorpusState, DiffEvent, DocumentEvent, FleetState,
    LocateResult, MeterEvent, Mode, PutCorpusRequest, Revision, Session, SessionEvent,
};
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

/// The open workspace (engine-sourced identity, ADR-0049 §2).
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct WorkspaceInfo {
    pub id: String,
    pub root: String,
    pub name: String,
}

/// The corpus scope field currently being edited (E2c).
#[derive(Debug, Clone, Copy, PartialEq, Eq, Default)]
pub enum ScopeField {
    #[default]
    Roots,
    Include,
    Exclude,
}

impl ScopeField {
    pub fn next(self) -> Self {
        match self {
            Self::Roots => Self::Include,
            Self::Include => Self::Exclude,
            Self::Exclude => Self::Roots,
        }
    }

    pub fn label(self) -> &'static str {
        match self {
            Self::Roots => "roots",
            Self::Include => "include",
            Self::Exclude => "exclude",
        }
    }
}

/// The in-progress corpus-scope editor (E2c). One buffer per wire list; Tab
/// cycles the active field and Enter persists all three via `PUT /corpus`.
#[derive(Debug, Clone, Default)]
pub struct ScopeEdit {
    pub field: ScopeField,
    pub roots: String,
    pub include: String,
    pub exclude: String,
}

impl ScopeEdit {
    /// Seed the editor from the engine's current scope (comma-joined).
    pub fn from_scope(corpus: Option<&CorpusState>) -> Self {
        let (roots, include, exclude) = match corpus {
            Some(c) => (
                c.roots.join(", "),
                c.include.join(", "),
                c.exclude.join(", "),
            ),
            None => (String::new(), String::new(), String::new()),
        };
        Self {
            field: ScopeField::Roots,
            roots,
            include,
            exclude,
        }
    }

    pub fn current(&self) -> &String {
        match self.field {
            ScopeField::Roots => &self.roots,
            ScopeField::Include => &self.include,
            ScopeField::Exclude => &self.exclude,
        }
    }

    pub fn current_mut(&mut self) -> &mut String {
        match self.field {
            ScopeField::Roots => &mut self.roots,
            ScopeField::Include => &mut self.include,
            ScopeField::Exclude => &mut self.exclude,
        }
    }

    pub fn next_field(&mut self) {
        self.field = self.field.next();
    }

    /// Build the wire request; comma-separated text → trimmed, non-empty lists.
    /// The client sends decisions, never payload text (ADR-0013 §3).
    pub fn request(&self, workspace_id: &str) -> PutCorpusRequest {
        PutCorpusRequest {
            workspace_id: workspace_id.to_string(),
            roots: Some(comma_list(&self.roots)),
            include: Some(comma_list(&self.include)),
            exclude: Some(comma_list(&self.exclude)),
        }
    }
}

/// Split a comma-separated editor value into trimmed, non-empty entries.
pub fn comma_list(value: &str) -> Vec<String> {
    value
        .split(',')
        .map(str::trim)
        .filter(|s| !s.is_empty())
        .map(str::to_string)
        .collect()
}

/// A display line for one corpus document's index status (E2c): status, chunk
/// count, and the labeled error when present.
pub fn corpus_status_label(doc: &CorpusDocument) -> String {
    let mut label = doc.status.as_str().to_string();
    if let Some(chunks) = doc.chunk_count {
        label.push_str(&format!(" · {chunks} chunks"));
    }
    if let Some(error) = &doc.error {
        label.push_str(&format!(" · {error}"));
    }
    label
}

/// A fleet lifecycle-action failure (E3). `provision_hint` is set when a start
/// was refused because the model is not yet provisioned (ADR-0040 §5).
#[derive(Debug, Clone, Default)]
pub struct FleetErrorInfo {
    pub message: String,
    pub provision_hint: bool,
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
    /// The resumed/created session (title + policy are engine-sourced).
    Session {
        id: String,
        title: Option<String>,
        policy: Option<crate::gen::ContextPolicy>,
    },
    /// The opened document's block tree (reader; ADR-0050).
    Blocks(Vec<crate::gen::Block>),
    /// The persisted session context policy after a tray save (E2).
    SessionPolicy(crate::gen::ContextPolicy),
    /// The resolved/created workspace (ADR-0049 §2).
    Workspace {
        id: String,
        root: String,
        name: String,
    },
    /// A workspace-scoped session list (ADR-0049 §5).
    Sessions(Vec<Session>),
    /// A bounded directory listing (ADR-0035/ADR-0049 §6).
    Directory {
        listing: crate::gen::DirectoryListing,
    },
    /// A typed `path-outside-allowed-roots` refusal (ADR-0049 §6) — rendered,
    /// never swallowed.
    DirectoryRefused {
        path: String,
        allowed_roots: Vec<String>,
    },
    /// The workspace corpus state (scope + per-document status + job) (E2c).
    Corpus(CorpusState),
    /// An accepted corpus indexing job, for immediate progress feedback (E2c).
    CorpusJob(CorpusJob),
    /// A corpus `path-outside-allowed-roots` refusal (scope/evict; ADR-0049 §6).
    CorpusRefused {
        path: String,
        allowed_roots: Vec<String>,
    },
    /// Fleet observability state (`GET /fleet`) (E3).
    Fleet(FleetState),
    /// A fleet lifecycle verb failed (E3); rendered with the implied remediation.
    FleetError(FleetErrorInfo),
    /// A `document` liveness-feed event (ADR-0052 §4): external change / commit.
    FeedDocument(DocumentEvent),
    /// A `session` liveness-feed event (ADR-0052 §4): create / rename.
    FeedSession(SessionEvent),
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

/// A modal overlay (E2): the bounded directory picker or the session list.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Default)]
pub enum Overlay {
    #[default]
    None,
    Directory,
    Sessions,
    Tray,
    Mentions,
    Locate,
    Corpus,
    Fleet,
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
    pub session_title: Option<String>,
    /// The active turn id, captured from the `turn`/`context`/`locate` events;
    /// address for the cancel and locate picker routes (E2).
    pub turn_id: Option<String>,
    /// True when the last turn ended `done {cancelled:true}`.
    pub cancelled: bool,
    /// Accumulated reasoning deltas for the thinking indicator (E2/C5).
    pub thinking: String,
    /// The open workspace (engine identity), when one is resolved (E2).
    pub workspace: Option<WorkspaceInfo>,
    /// The workspace-scoped session list (E2).
    pub sessions: Vec<Session>,
    /// The most recent directory listing (the file/mention picker) (E2).
    pub directory: Option<crate::gen::DirectoryListing>,
    /// The most recent context snapshot (inspector; E2). Engine data, rendered.
    pub last_context: Option<ContextSnapshot>,
    /// The workspace corpus state (corpus tree; E2c). Engine data, rendered.
    pub corpus: Option<CorpusState>,
    /// The selected corpus document in the overlay (E2c).
    pub corpus_index: usize,
    /// The in-progress corpus-scope edit (E2c).
    pub scope_edit: Option<ScopeEdit>,
    /// The fleet observability state (E3). Engine data, rendered.
    pub fleet: Option<FleetState>,
    /// The selected fleet model in the overlay (E3).
    pub fleet_index: usize,
    /// The last fleet lifecycle-verb failure (E3).
    pub fleet_error: Option<FleetErrorInfo>,
    /// The most recent `/locate` outcome (E2).
    pub last_locate: Option<LocateResult>,
    /// The pending `@`-mention attachments for the next turn (absolute paths).
    pub mentions: Vec<String>,
    /// The opened document's block tree (reader view-model; ADR-0050 §4).
    pub blocks: Vec<crate::gen::Block>,
    /// Set by a `document` feed event (external change / commit) so the render
    /// loop issues one `GET /documents/{id}/blocks` refresh (never a poll).
    pub needs_blocks_reload: bool,
    /// Reader pane visibility (ADR-0050 §4).
    pub reader_visible: bool,
    /// Inspector pane visibility (meter + context; ADR-0044).
    pub inspector_visible: bool,
    /// The selected chunk in the context tray.
    pub tray_index: usize,
    /// The persisted session context policy (engine-sourced; E2 tray).
    pub session_policy: Option<crate::gen::ContextPolicy>,
    /// The in-progress retrieval-query edit.
    pub query_edit: Option<String>,
    /// When set, the next turn carries the tray policy as a per-turn override.
    pub override_next_turn: bool,
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
    /// The open modal overlay (E2).
    pub overlay: Overlay,
    /// The selected row in the active overlay.
    pub picker_index: usize,
    /// The in-progress session title edit (Some while renaming).
    pub rename: Option<String>,
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
        self.cancelled = false;
        self.thinking.clear();
        self.turn_id = None;
        self.last_locate = None;
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
                self.overlay = Overlay::None;
                if external_change {
                    self.note("external-change: file re-read from disk on open".to_string());
                }
            }
            UiEvent::Session { id, title, policy } => {
                self.session_id = Some(id);
                self.session_title = title;
                self.session_policy = policy;
                self.tray_index = 0;
            }
            UiEvent::Blocks(blocks) => {
                self.blocks = blocks;
            }
            UiEvent::SessionPolicy(policy) => {
                self.session_policy = Some(policy);
            }
            UiEvent::Workspace { id, root, name } => {
                self.workspace = Some(WorkspaceInfo { id, root, name });
            }
            UiEvent::Sessions(sessions) => {
                self.sessions = sessions;
            }
            UiEvent::Directory { listing } => {
                self.directory = Some(listing);
                // A first listing with no document open is the bootstrap picker.
                if self.document_id.is_none() {
                    self.overlay = Overlay::Directory;
                    self.picker_index = 0;
                }
            }
            UiEvent::DirectoryRefused {
                path,
                allowed_roots,
            } => {
                // A typed refusal is rendered verbatim, never swallowed.
                let allowed = if allowed_roots.is_empty() {
                    "(none configured)".to_string()
                } else {
                    allowed_roots.join(", ")
                };
                self.error = Some(format!(
                    "path-outside-allowed-roots: {path} (allowed: {allowed})"
                ));
                self.note(format!("path-outside-allowed-roots: {path}"));
            }
            UiEvent::Corpus(state) => {
                self.corpus = Some(state);
                // Keep the selection in range across a refresh (never reset it
                // here — the poll must not move the cursor).
                let len = self.corpus.as_ref().map_or(0, |c| c.documents.len());
                if self.corpus_index >= len {
                    self.corpus_index = len.saturating_sub(1);
                }
            }
            UiEvent::CorpusJob(job) => {
                if let Some(corpus) = self.corpus.as_mut() {
                    corpus.job = Some(job);
                }
            }
            UiEvent::CorpusRefused {
                path,
                allowed_roots,
            } => {
                // A typed refusal is rendered verbatim, never swallowed.
                let allowed = if allowed_roots.is_empty() {
                    "(none configured)".to_string()
                } else {
                    allowed_roots.join(", ")
                };
                self.error = Some(format!(
                    "path-outside-allowed-roots: {path} (allowed: {allowed})"
                ));
                self.note(format!("path-outside-allowed-roots: {path}"));
            }
            UiEvent::Fleet(state) => {
                self.fleet = Some(state);
                self.fleet_error = None;
                let len = self.fleet.as_ref().map_or(0, |f| f.models.len());
                if self.fleet_index >= len {
                    self.fleet_index = len.saturating_sub(1);
                }
            }
            UiEvent::FleetError(error) => {
                self.fleet_error = Some(error);
            }
            UiEvent::FeedDocument(event) => {
                // Only react to the document this client has open.
                if self.document_id.as_deref() == Some(event.document_id.as_str()) {
                    match event.kind {
                        crate::gen::DocumentEventKind::ExternalChange => {
                            self.note("external-change: file changed on disk".to_string());
                            self.needs_blocks_reload = true;
                        }
                        crate::gen::DocumentEventKind::Commit => {
                            self.needs_blocks_reload = true;
                        }
                    }
                }
            }
            UiEvent::FeedSession(event) => {
                if let Some(pos) = self.sessions.iter().position(|s| s.id == event.session.id) {
                    self.sessions[pos] = event.session.clone();
                } else {
                    self.sessions.insert(0, event.session.clone());
                }
                if self.session_id.as_deref() == Some(event.session.id.as_str()) {
                    self.session_title = event.session.title.clone();
                    self.session_policy = event.session.context_policy.clone();
                }
            }
            UiEvent::Turn(SseEvent::Turn(turn)) => {
                self.turn_id = Some(turn.turn_id.clone());
                if self.session_id.is_none() {
                    self.session_id = Some(turn.session_id.clone());
                }
            }
            UiEvent::Turn(SseEvent::Locate(locate)) => {
                if let Some(turn_id) = &locate.turn_id {
                    self.turn_id = Some(turn_id.clone());
                }
                // An ambiguous outcome opens the picker; the turn waits for the
                // choice (ADR-0048 §4).
                if matches!(locate.status, crate::gen::LocateResultStatus::Ambiguous) {
                    self.overlay = Overlay::Locate;
                    self.picker_index = 0;
                }
                self.last_locate = Some(locate);
            }
            UiEvent::Turn(SseEvent::Thinking(thinking)) => {
                self.thinking.push_str(&thinking.text);
            }
            UiEvent::Turn(SseEvent::Context(context)) => {
                self.turn_id = Some(context.turn_id.clone());
                self.last_context = Some(context);
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
            UiEvent::Turn(SseEvent::Rag(_)) => {
                // The rag event is rendered through the last context snapshot
                // (inspector); E2b owns the dedicated RAG pane.
            }
            UiEvent::Turn(SseEvent::Done(done)) => {
                self.status.used_model = done.used_model;
                self.status.degraded = done.degraded.unwrap_or(false);
                self.cancelled = done.cancelled.unwrap_or(false);
                if self.cancelled {
                    self.note("turn cancelled".to_string());
                }
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
            cancelled: None,
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

    fn turn_event(turn_id: &str, session_id: &str) -> UiEvent {
        UiEvent::Turn(SseEvent::Turn(crate::gen::TurnEvent {
            turn_id: turn_id.to_string(),
            session_id: session_id.to_string(),
        }))
    }

    #[test]
    fn turn_event_captures_the_turn_id() {
        let mut app = AppState::default();
        app.reduce(turn_event("t1", "s1"));
        assert_eq!(app.turn_id.as_deref(), Some("t1"));
        assert_eq!(app.session_id.as_deref(), Some("s1"));
    }

    #[test]
    fn locate_event_captures_the_turn_id() {
        let mut app = AppState::default();
        app.reduce(UiEvent::Turn(SseEvent::Locate(crate::gen::LocateResult {
            block_id: None,
            candidates: None,
            chunk_key: None,
            confidence: None,
            context: None,
            document_id: None,
            match_type: None,
            path: None,
            span: None,
            stale: None,
            status: crate::gen::LocateResultStatus::Ambiguous,
            turn_id: Some("t9".to_string()),
        })));
        assert_eq!(app.turn_id.as_deref(), Some("t9"));
        assert!(app.last_locate.is_some());
    }

    #[test]
    fn ambiguous_locate_opens_the_picker() {
        let mut app = AppState::default();
        app.reduce(UiEvent::Turn(SseEvent::Locate(crate::gen::LocateResult {
            block_id: None,
            candidates: Some(vec![crate::gen::LocateCandidate {
                block_id: None,
                chunk_key: "/v/a.md#0".to_string(),
                document_id: None,
                path: "/v/a.md".to_string(),
                score: 0.9,
                stale: None,
                text_preview: "…".to_string(),
            }]),
            chunk_key: None,
            confidence: None,
            context: None,
            document_id: None,
            match_type: None,
            path: None,
            span: None,
            stale: None,
            status: crate::gen::LocateResultStatus::Ambiguous,
            turn_id: Some("t1".to_string()),
        })));
        assert_eq!(app.overlay, Overlay::Locate);
        assert_eq!(app.picker_index, 0);
    }

    #[test]
    fn done_cancelled_marks_the_turn_cancelled() {
        let mut app = AppState::default();
        app.begin_turn("hi".to_string());
        app.reduce(turn_event("t1", "s1"));
        app.reduce(UiEvent::Turn(SseEvent::Done(crate::gen::DoneEvent {
            cancelled: Some(true),
            degraded: None,
            used_model: Some("gemma".to_string()),
        })));
        assert!(app.cancelled);
        assert!(!app.turn_active);
    }

    #[test]
    fn thinking_deltas_accumulate() {
        let mut app = AppState::default();
        app.begin_turn("hi".to_string());
        app.reduce(UiEvent::Turn(SseEvent::Thinking(
            crate::gen::ThinkingEvent {
                text: "pon".to_string(),
            },
        )));
        app.reduce(UiEvent::Turn(SseEvent::Thinking(
            crate::gen::ThinkingEvent {
                text: "der".to_string(),
            },
        )));
        assert_eq!(app.thinking, "ponder");
    }

    #[test]
    fn blocks_event_populates_the_reader_view() {
        let mut app = AppState::default();
        app.reduce(UiEvent::Blocks(vec![crate::gen::Block {
            id: "b1".to_string(),
            parent_id: None,
            kind: crate::gen::BlockKind::Paragraph,
            position: 0,
            text: "Hello".to_string(),
            hash: None,
        }]));
        assert_eq!(app.blocks.len(), 1);
        assert_eq!(app.blocks[0].text, "Hello");
    }

    #[test]
    fn session_event_carries_the_context_policy() {
        let mut app = AppState::default();
        app.reduce(UiEvent::Session {
            id: "s1".to_string(),
            title: Some("Draft".to_string()),
            policy: Some(crate::gen::ContextPolicy {
                auto_rag: Some(false),
                ..Default::default()
            }),
        });
        assert_eq!(app.session_id.as_deref(), Some("s1"));
        assert_eq!(app.session_title.as_deref(), Some("Draft"));
        assert_eq!(app.session_policy.as_ref().unwrap().auto_rag, Some(false));
    }

    #[test]
    fn directory_refusal_is_rendered_verbatim() {
        let mut app = AppState::default();
        app.reduce(UiEvent::DirectoryRefused {
            path: "/etc".to_string(),
            allowed_roots: vec!["/home/me".to_string()],
        });
        let err = app.error.as_deref().unwrap_or_default();
        assert!(err.starts_with("path-outside-allowed-roots: /etc"));
        assert!(err.contains("/home/me"));
    }

    fn corpus_doc(
        id: &str,
        status: crate::gen::CorpusDocumentStatus,
    ) -> crate::gen::CorpusDocument {
        crate::gen::CorpusDocument {
            chunk_count: Some(3),
            error: None,
            id: id.to_string(),
            indexed_at: Some(1),
            path: format!("/v/{id}.md"),
            status,
        }
    }

    fn corpus_state(docs: Vec<crate::gen::CorpusDocument>) -> CorpusState {
        CorpusState {
            documents: docs,
            exclude: vec![],
            include: vec!["**/*.md".to_string()],
            job: None,
            roots: vec!["/v".to_string()],
            workspace_id: "w1".to_string(),
        }
    }

    #[test]
    fn corpus_event_populates_state_and_clamps_selection() {
        let mut app = AppState::default();
        app.corpus_index = 9;
        app.reduce(UiEvent::Corpus(corpus_state(vec![
            corpus_doc("a", crate::gen::CorpusDocumentStatus::Indexed),
            corpus_doc("b", crate::gen::CorpusDocumentStatus::Stale),
        ])));
        assert_eq!(app.corpus.as_ref().unwrap().documents.len(), 2);
        assert_eq!(app.corpus_index, 1);
    }

    #[test]
    fn corpus_job_reduces_progress() {
        let mut app = AppState::default();
        app.reduce(UiEvent::Corpus(corpus_state(vec![])));
        app.reduce(UiEvent::CorpusJob(crate::gen::CorpusJob {
            completed: 4,
            error: None,
            finished_at: None,
            id: "j1".to_string(),
            kind: crate::gen::CorpusJobKind::Index,
            started_at: Some(1),
            state: crate::gen::CorpusJobState::Running,
            total: 10,
            workspace_id: "w1".to_string(),
        }));
        let job = app.corpus.as_ref().unwrap().job.as_ref().unwrap();
        assert_eq!((job.completed, job.total), (4, 10));
        assert_eq!(job.state, crate::gen::CorpusJobState::Running);
    }

    #[test]
    fn corpus_refusal_is_rendered_verbatim() {
        let mut app = AppState::default();
        app.reduce(UiEvent::CorpusRefused {
            path: "/etc".to_string(),
            allowed_roots: vec!["/home/me".to_string()],
        });
        let err = app.error.as_deref().unwrap_or_default();
        assert!(err.starts_with("path-outside-allowed-roots: /etc"));
        assert!(err.contains("/home/me"));
    }

    #[test]
    fn corpus_status_label_renders_status_chunks_and_error() {
        assert_eq!(
            corpus_status_label(&corpus_doc("a", crate::gen::CorpusDocumentStatus::Indexed)),
            "indexed · 3 chunks"
        );
        assert_eq!(
            corpus_status_label(&corpus_doc("b", crate::gen::CorpusDocumentStatus::Stale)),
            "stale · 3 chunks"
        );
        let mut err = corpus_doc("c", crate::gen::CorpusDocumentStatus::Error);
        err.error = Some("boom".to_string());
        assert_eq!(corpus_status_label(&err), "error · 3 chunks · boom");
    }

    #[test]
    fn scope_edit_builds_the_request_and_cycles_fields() {
        let mut edit = ScopeEdit::from_scope(Some(&CorpusState {
            documents: vec![],
            exclude: vec!["**/.obsidian/**".to_string()],
            include: vec!["**/*.md".to_string()],
            job: None,
            roots: vec!["/v".to_string()],
            workspace_id: "w1".to_string(),
        }));
        assert_eq!(edit.field, ScopeField::Roots);
        edit.next_field();
        assert_eq!(edit.field, ScopeField::Include);
        edit.next_field();
        assert_eq!(edit.field, ScopeField::Exclude);
        edit.next_field();
        assert_eq!(edit.field, ScopeField::Roots);

        let mut edit = ScopeEdit {
            field: ScopeField::Roots,
            roots: " /v , /lit ,, ".to_string(),
            include: " **/*.md ".to_string(),
            exclude: "".to_string(),
        };
        let request = edit.request("w1");
        assert_eq!(request.workspace_id, "w1");
        assert_eq!(request.roots.unwrap(), vec!["/v", "/lit"]);
        assert_eq!(request.include.unwrap(), vec!["**/*.md"]);
        assert!(request.exclude.unwrap().is_empty());
        // The active buffer is the roots field.
        edit.current_mut().push_str(", /extra");
        assert!(edit.roots.contains("/extra"));
    }

    #[test]
    fn comma_list_trims_and_drops_empties() {
        assert_eq!(comma_list(" a , b ,, c "), vec!["a", "b", "c"]);
        assert!(comma_list("  ,  ").is_empty());
    }

    fn fleet_state(control: crate::gen::FleetStateControl) -> FleetState {
        FleetState {
            control,
            models: vec![crate::gen::FleetModel {
                base_url: "http://127.0.0.1:8001/v1".to_string(),
                capabilities: None,
                live_state: crate::gen::FleetModelLiveState::Down,
                mode_tags: Some(vec!["editor".to_string()]),
                name: "gemma4-12b".to_string(),
            }],
        }
    }

    #[test]
    fn fleet_event_populates_state_and_clears_error() {
        let mut app = AppState::default();
        app.fleet_error = Some(FleetErrorInfo {
            message: "old".to_string(),
            provision_hint: true,
        });
        app.fleet_index = 9;
        app.reduce(UiEvent::Fleet(fleet_state(
            crate::gen::FleetStateControl::Up,
        )));
        assert_eq!(
            app.fleet.as_ref().unwrap().control,
            crate::gen::FleetStateControl::Up
        );
        assert_eq!(app.fleet_index, 0);
        assert!(app.fleet_error.is_none());
    }

    #[test]
    fn fleet_unreachable_is_data_not_an_error() {
        let mut app = AppState::default();
        app.reduce(UiEvent::Fleet(fleet_state(
            crate::gen::FleetStateControl::Unreachable,
        )));
        let fleet = app.fleet.as_ref().unwrap();
        assert_eq!(fleet.control.as_str(), "unreachable");
        assert_eq!(fleet.models[0].live_state.as_str(), "down");
    }

    #[test]
    fn fleet_error_carries_the_provision_hint() {
        let mut app = AppState::default();
        app.reduce(UiEvent::FleetError(FleetErrorInfo {
            message: "fleet start x: model-not-found".to_string(),
            provision_hint: true,
        }));
        let err = app.fleet_error.as_ref().unwrap();
        assert!(err.provision_hint);
        assert!(err.message.contains("model-not-found"));
    }
}
