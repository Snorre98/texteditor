//! The async bridge (ADR-0046 §4).
//!
//! One tokio runtime on a worker OS thread owns the generated async calls and
//! the `/turn` byte stream. The UI thread stays synchronous and render-only and
//! exchanges messages with the worker over channels:
//!
//! - **UI → worker:** [`Command`] over a `tokio::sync::mpsc::UnboundedSender`
//!   (a synchronous `send`).
//! - **worker → UI:** [`UiEvent`] over a `std::sync::mpsc::Sender`, drained by
//!   the UI with `try_recv` each frame.
//!
//! Each turn runs in its own spawned task (E2) so the command loop stays
//! responsive while a turn streams — that is what makes `Command::Cancel`
//! deliverable. The worker owns the immutable engine facts (base URL, workspace
//! id, open document id, session id); the UI supplies only the active preset
//! name and the typed message per turn. No domain logic runs here — it routes
//! bytes and events.

use std::path::Path;
use std::sync::mpsc::{self, Receiver, Sender};
use std::thread;

use futures_util::StreamExt;

use crate::discovery::{self, EngineEnv};
use crate::gen::{
    BlockEdit, CancelTurnApiError, CommitDocumentApiError, CommitRequest, CreateSessionRequest,
    CreateWorkspaceRequest, HttpClient, ListDirectoryApiError, OpenDocumentRequest,
    RenameSessionRequest, Session, Task,
};
use crate::sse::{Decoded, SseDecoder};
use crate::state::UiEvent;

/// A request from the UI thread to the worker.
#[derive(Debug, Clone)]
pub enum Command {
    /// Resolve-or-create a workspace for `path`; if it is a directory, show the
    /// bounded listing, otherwise open it as a document (E2).
    Bootstrap { path: String },
    /// List one directory (bounded by ALLOWED_ROOTS).
    ListDirectory { path: String },
    /// Open a document (chosen from the directory picker) and resume/create its
    /// session.
    OpenDocument { path: String },
    /// Refresh the workspace-scoped session list.
    ListSessions,
    /// Resume an existing session.
    OpenSession { session: Session },
    /// Create a new doc-level session for the active document.
    NewSession,
    /// Rename a session.
    RenameSession { session_id: String, title: String },
    /// Run one turn with the active preset.
    SendTurn { mode: String, input: String },
    /// Cancel the running turn by id (POST /turns/{id}/cancel).
    Cancel { turn_id: String },
    /// Accept the staged candidate for `block_id` (stage + commit + write-through).
    Approve { block_id: String },
    /// Retry an approve with the explicit `overwrite: true` opt-in.
    Overwrite { block_id: String },
    /// Ask the worker to stop.
    Shutdown,
}

/// The worker handle held by the UI: a command sender and the event receiver.
pub struct Bridge {
    pub cmd_tx: tokio::sync::mpsc::UnboundedSender<Command>,
    pub event_rx: Receiver<UiEvent>,
}

impl Bridge {
    /// Send a command; errors only if the worker has stopped.
    pub fn send(&self, command: Command) {
        let _ = self.cmd_tx.send(command);
    }

    /// Ask the worker to exit.
    pub fn shutdown(&self) {
        self.send(Command::Shutdown);
    }
}

/// Spawn the worker thread and return the UI-side handle.
pub fn spawn(env: EngineEnv) -> Bridge {
    let (cmd_tx, cmd_rx) = tokio::sync::mpsc::unbounded_channel::<Command>();
    let (event_tx, event_rx) = mpsc::channel::<UiEvent>();
    thread::Builder::new()
        .name("tui-engine".to_string())
        .spawn(move || {
            let runtime = tokio::runtime::Builder::new_multi_thread()
                .enable_all()
                .build()
                .expect("build tokio runtime");
            runtime.block_on(run_worker(env, cmd_rx, event_tx));
        })
        .expect("spawn tui-engine worker thread");
    Bridge { cmd_tx, event_rx }
}

async fn run_worker(
    env: EngineEnv,
    mut cmd_rx: tokio::sync::mpsc::UnboundedReceiver<Command>,
    event_tx: Sender<UiEvent>,
) {
    let client = match discovery::discover(&env).await {
        Ok(base_url) => {
            let _ = event_tx.send(UiEvent::Discovered {
                base_url: base_url.clone(),
            });
            Some(HttpClient::new().with_base_url(base_url))
        }
        Err(err) => {
            let _ = event_tx.send(UiEvent::ConnectFailed(err.to_string()));
            None
        }
    };

    let mut worker = Worker {
        event_tx: event_tx.clone(),
        client,
        workspace_id: None,
        doc_id: None,
        session_id: None,
    };

    // The running turn's task handle (at most one turn at a time — the UI
    // guards this). Kept so Shutdown can abort an in-flight stream.
    let mut active: Option<tokio::task::JoinHandle<()>> = None;

    while let Some(command) = cmd_rx.recv().await {
        match command {
            Command::Bootstrap { path } => worker.bootstrap(path).await,
            Command::ListDirectory { path } => worker.list_directory(path).await,
            Command::OpenDocument { path } => worker.open_document(path).await,
            Command::ListSessions => worker.refresh_sessions().await,
            Command::OpenSession { session } => worker.open_session(session),
            Command::NewSession => worker.new_session().await,
            Command::RenameSession { session_id, title } => {
                worker.rename_session(session_id, title).await
            }
            Command::SendTurn { mode, input } => match worker.build_turn_task(mode, input) {
                Some(task) => {
                    if let Some(client) = worker.client.clone() {
                        let tx = worker.event_tx.clone();
                        active = Some(tokio::spawn(pump_turn(client, task, tx)));
                    }
                }
                None => {
                    worker.error("session not ready; turn ignored");
                    worker.emit(UiEvent::TurnEnded);
                }
            },
            Command::Cancel { turn_id } => worker.cancel(turn_id).await,
            Command::Approve { block_id } => worker.approve(block_id, false).await,
            Command::Overwrite { block_id } => worker.approve(block_id, true).await,
            Command::Shutdown => {
                if let Some(handle) = active.take() {
                    handle.abort();
                }
                break;
            }
        }
    }
}

struct Worker {
    event_tx: Sender<UiEvent>,
    client: Option<HttpClient>,
    workspace_id: Option<String>,
    doc_id: Option<String>,
    session_id: Option<String>,
}

impl Worker {
    fn emit(&self, event: UiEvent) {
        let _ = self.event_tx.send(event);
    }

    fn error(&self, message: impl Into<String>) {
        self.emit(UiEvent::Error(message.into()));
    }

    /// Resolve-or-create a workspace for `path` (E2). A directory opens the
    /// bounded browser; a file opens as a document and resumes its session.
    async fn bootstrap(&mut self, path: String) {
        if self.client.is_none() {
            self.error("engine not connected; cannot open");
            return;
        }
        // Try the bounded directory listing first: a directory lists, a file
        // does not. `ListDirectoryApiError::Status403` is the typed refusal.
        match self.client.clone().unwrap().list_directory(&path).await {
            Ok(listing) => {
                self.adopt_workspace(path).await;
                self.emit(UiEvent::Directory { listing });
            }
            Err(err) => {
                if let Some(api) = err.api() {
                    if let Some(ListDirectoryApiError::Status403(refusal)) = &api.typed {
                        self.emit(UiEvent::DirectoryRefused {
                            path: refusal.path.clone(),
                            allowed_roots: refusal.allowed_roots.clone(),
                        });
                        return;
                    }
                }
                self.open_document(path).await;
            }
        }
    }

    /// Resolve-or-create a workspace rooted at `root` and emit it.
    async fn adopt_workspace(&mut self, root: String) {
        let Some(client) = self.client.clone() else {
            return;
        };
        match client
            .create_workspace(CreateWorkspaceRequest { root, name: None })
            .await
        {
            Ok(ws) => {
                self.workspace_id = Some(ws.id.clone());
                self.emit(UiEvent::Workspace {
                    id: ws.id,
                    root: ws.root,
                    name: ws.name,
                });
            }
            Err(err) => self.error(format!("open workspace failed: {err}")),
        }
    }

    /// Open a document by path, resolve its workspace, load presets, and
    /// resume/create its session (E1 bootstrap, E2 workspace-aware).
    async fn open_document(&mut self, path: String) {
        let Some(client) = self.client.clone() else {
            self.error("engine not connected; cannot open document");
            return;
        };

        let document = match client.open_document(OpenDocumentRequest { path }).await {
            Ok(document) => document,
            Err(err) => {
                self.error(format!("open document failed: {err}"));
                return;
            }
        };
        self.doc_id = Some(document.id.clone());
        self.emit(UiEvent::Document {
            id: document.id.clone(),
            path: document.path.clone(),
            external_change: document.external_change.unwrap_or(false),
        });

        // Resolve the workspace from the canonical document parent (string path
        // arithmetic on engine-provided data; no client filesystem read).
        if self.workspace_id.is_none() {
            if let Some(parent) = Path::new(&document.path).parent() {
                self.adopt_workspace(parent.to_string_lossy().into_owned())
                    .await;
            }
        }

        match client.list_modes().await {
            Ok(modes) => self.emit(UiEvent::Modes(modes)),
            Err(err) => self.error(format!("list presets failed: {err}")),
        }

        self.resume_or_create(&document.id).await;
    }

    /// List one directory (bounded).
    async fn list_directory(&self, path: String) {
        let Some(client) = self.client.clone() else {
            return;
        };
        match client.list_directory(&path).await {
            Ok(listing) => self.emit(UiEvent::Directory { listing }),
            Err(err) => {
                if let Some(api) = err.api() {
                    if let Some(ListDirectoryApiError::Status403(refusal)) = &api.typed {
                        self.emit(UiEvent::DirectoryRefused {
                            path: refusal.path.clone(),
                            allowed_roots: refusal.allowed_roots.clone(),
                        });
                        return;
                    }
                }
                self.error(format!("list directory failed: {err}"));
            }
        }
    }

    /// Refresh the workspace-scoped (optionally document-filtered) session list.
    async fn refresh_sessions(&self) {
        let Some(client) = self.client.clone() else {
            return;
        };
        match client
            .list_sessions(self.workspace_id.clone(), self.doc_id.clone())
            .await
        {
            Ok(sessions) => self.emit(UiEvent::Sessions(sessions)),
            Err(err) => self.error(format!("list sessions failed: {err}")),
        }
    }

    /// Resume the newest session for a document, or create one.
    async fn resume_or_create(&mut self, document_id: &str) {
        let Some(client) = self.client.clone() else {
            return;
        };
        match client
            .list_sessions(self.workspace_id.clone(), Some(document_id.to_string()))
            .await
        {
            Ok(sessions) => {
                self.emit(UiEvent::Sessions(sessions.clone()));
                if let Some(session) = sessions.into_iter().next() {
                    self.session_id = Some(session.id.clone());
                    self.emit(UiEvent::Session {
                        id: session.id,
                        title: session.title,
                    });
                } else {
                    self.create_session(document_id, None).await;
                }
            }
            Err(err) => {
                self.error(format!("list sessions failed: {err}"));
                self.create_session(document_id, None).await;
            }
        }
    }

    async fn create_session(&mut self, document_id: &str, title: Option<String>) {
        let Some(client) = self.client.clone() else {
            return;
        };
        let request = CreateSessionRequest {
            anchor_block_id: None,
            document_id: document_id.to_string(),
            mode_type: None,
            title,
            workspace_id: self.workspace_id.clone(),
        };
        match client.create_session(request).await {
            Ok(session) => {
                self.session_id = Some(session.id.clone());
                self.emit(UiEvent::Session {
                    id: session.id,
                    title: session.title,
                });
            }
            Err(err) => self.error(format!("create session failed: {err}")),
        }
    }

    /// Create a new doc-level session for the active document.
    async fn new_session(&mut self) {
        let Some(document_id) = self.doc_id.clone() else {
            self.error("no document open; cannot create a session");
            return;
        };
        self.create_session(&document_id, None).await;
    }

    /// Resume a session chosen from the list.
    fn open_session(&mut self, session: Session) {
        self.session_id = Some(session.id.clone());
        self.doc_id = Some(session.document_id.clone());
        self.emit(UiEvent::Session {
            id: session.id,
            title: session.title,
        });
    }

    async fn rename_session(&mut self, session_id: String, title: String) {
        let Some(client) = self.client.clone() else {
            return;
        };
        match client
            .rename_session(&session_id, RenameSessionRequest { title })
            .await
        {
            Ok(session) => {
                if self.session_id.as_deref() == Some(session.id.as_str()) {
                    self.emit(UiEvent::Session {
                        id: session.id.clone(),
                        title: session.title.clone(),
                    });
                }
                self.refresh_sessions().await;
            }
            Err(err) => self.error(format!("rename session failed: {err}")),
        }
    }

    /// Build the turn Task from the worker's engine facts.
    fn build_turn_task(&self, mode: String, input: String) -> Option<Task> {
        let document_id = self.doc_id.clone()?;
        let session_id = self.session_id.clone()?;
        let mut task = Task::new(document_id, mode, session_id, input);
        task.workspace_id = self.workspace_id.clone();
        Some(task)
    }

    /// Cancel a turn by id. A typed 404/409 is labeled; the turn's own terminal
    /// `done {cancelled:true}` arrives on its `/turn` stream.
    async fn cancel(&self, turn_id: String) {
        let Some(client) = self.client.clone() else {
            return;
        };
        match client.cancel_turn(&turn_id).await {
            Ok(()) => {}
            Err(err) => {
                if let Some(api) = err.api() {
                    match &api.typed {
                        Some(CancelTurnApiError::Status409(_)) => {
                            self.error("cancel: turn is not running")
                        }
                        Some(CancelTurnApiError::Status404(_)) => {
                            self.error("cancel: unknown turn")
                        }
                        None => self.error(format!("cancel failed: {err}")),
                    }
                } else {
                    self.error(format!("cancel failed: {err}"));
                }
            }
        }
    }

    /// The diff-preview accept path: fetch the newest staged candidate, apply it,
    /// then commit (which is the write boundary, ADR-0047). `overwrite` is the
    /// explicit external-change opt-in.
    async fn approve(&mut self, block_id: String, overwrite: bool) {
        let (Some(client), Some(document_id)) = (self.client.clone(), self.doc_id.clone()) else {
            self.error("document not ready; approve ignored");
            return;
        };

        let candidates = match client.get_candidates(&document_id, &block_id).await {
            Ok(candidates) => candidates,
            Err(err) => {
                self.error(format!("list candidates failed: {err}"));
                return;
            }
        };
        // Engine returns candidates newest-first (ts DESC, rowid DESC — ADR-0047 §5).
        let Some(text) = candidates.into_iter().next().and_then(|c| c.text) else {
            self.error(format!("no candidate staged for block {block_id}"));
            return;
        };

        if let Err(err) = client
            .apply_edit(&document_id, BlockEdit::new(block_id, text))
            .await
        {
            self.error(format!("apply edit failed: {err}"));
            return;
        }

        let request = CommitRequest {
            overwrite: Some(overwrite),
        };
        match client.commit_document(&document_id, Some(request)).await {
            Ok(revision) => self.emit(UiEvent::Approved { revision }),
            Err(err) => {
                if let Some(api) = err.api() {
                    if let Some(CommitDocumentApiError::Status409(conflict)) = &api.typed {
                        self.emit(UiEvent::Conflict {
                            message: format!("file-changed-externally: {}", conflict.path),
                        });
                        return;
                    }
                }
                self.error(format!("commit failed: {err}"));
            }
        }
    }
}

/// Drive one turn's SSE stream to completion, forwarding typed events to the UI.
/// Runs in its own task so the worker command loop can service Cancel.
async fn pump_turn(client: HttpClient, task: Task, event_tx: Sender<UiEvent>) {
    let stream = match client.start_turn(task).await {
        Ok(stream) => stream,
        Err(err) => {
            let _ = event_tx.send(UiEvent::Error(format!("turn failed: {err}")));
            let _ = event_tx.send(UiEvent::TurnEnded);
            return;
        }
    };

    futures_util::pin_mut!(stream);
    let mut decoder = SseDecoder::new();
    let mut terminal = false;
    while let Some(chunk) = stream.next().await {
        match chunk {
            Ok(bytes) => {
                for decoded in decoder.push(&bytes) {
                    terminal |= emit_decoded(&event_tx, decoded);
                }
            }
            Err(err) => {
                let _ = event_tx.send(UiEvent::Error(format!("turn stream error: {err}")));
                break;
            }
        }
        if terminal {
            break;
        }
    }
    if !terminal {
        if let Some(decoded) = decoder.finish() {
            emit_decoded(&event_tx, decoded);
        }
    }
    let _ = event_tx.send(UiEvent::TurnEnded);
}

/// Emit one decoded event; returns true when it is terminal.
fn emit_decoded(event_tx: &Sender<UiEvent>, decoded: Decoded) -> bool {
    match decoded {
        Decoded::Event(event) => {
            let terminal = event.is_terminal();
            let _ = event_tx.send(UiEvent::Turn(event));
            terminal
        }
        Decoded::Unknown { name } => {
            let _ = event_tx.send(UiEvent::ProtocolNote(format!(
                "unknown SSE event `{name}` labeled and skipped"
            )));
            false
        }
        Decoded::Invalid { name, error } => {
            let _ = event_tx.send(UiEvent::ProtocolNote(format!(
                "invalid SSE `{name}` event labeled and skipped: {error}"
            )));
            false
        }
    }
}
