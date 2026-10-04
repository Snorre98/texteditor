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
//! The worker owns the immutable engine facts (base URL, open document id,
//! session id); the UI supplies only the active preset name and the typed
//! message per turn. No domain logic runs here — it routes bytes and events.

use std::sync::mpsc::{self, Receiver, Sender};
use std::thread;

use futures_util::StreamExt;

use crate::discovery::{self, EngineEnv};
use crate::gen::{
    BlockEdit, CommitDocumentApiError, CommitRequest, CreateSessionRequest, HttpClient,
    OpenDocumentRequest, Task,
};
use crate::sse::{Decoded, SseDecoder};
use crate::state::UiEvent;

/// A request from the UI thread to the worker.
#[derive(Debug, Clone)]
pub enum Command {
    /// Open `path`, load presets, and resume/create a session.
    Bootstrap { path: String },
    /// Run one turn with the active preset.
    SendTurn { mode: String, input: String },
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
        event_tx,
        client,
        doc_id: None,
        session_id: None,
    };

    while let Some(command) = cmd_rx.recv().await {
        match command {
            Command::Bootstrap { path } => worker.bootstrap(path).await,
            Command::SendTurn { mode, input } => worker.run_turn(mode, input).await,
            Command::Approve { block_id } => worker.approve(block_id, false).await,
            Command::Overwrite { block_id } => worker.approve(block_id, true).await,
            Command::Shutdown => break,
        }
    }
}

struct Worker {
    event_tx: Sender<UiEvent>,
    client: Option<HttpClient>,
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

    /// Open the document, load presets, and resume the newest session (or create
    /// one). `Task.workspaceId` stays absent — the engine's canonical-parent
    /// fallback covers pre-workspace flows (ADR-0049 §5).
    async fn bootstrap(&mut self, path: String) {
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

        match client.list_modes().await {
            Ok(modes) => self.emit(UiEvent::Modes(modes)),
            Err(err) => self.error(format!("list presets failed: {err}")),
        }

        match client
            .list_sessions(None::<String>, Some(document.id.clone()))
            .await
        {
            Ok(sessions) => {
                if let Some(session) = sessions.into_iter().next() {
                    self.session_id = Some(session.id.clone());
                    self.emit(UiEvent::Session { id: session.id });
                } else {
                    self.create_session(&client, &document.id).await;
                }
            }
            Err(err) => {
                self.error(format!("list sessions failed: {err}"));
                self.create_session(&client, &document.id).await;
            }
        }
    }

    async fn create_session(&mut self, client: &HttpClient, document_id: &str) {
        let request = CreateSessionRequest {
            anchor_block_id: None,
            document_id: document_id.to_string(),
            mode_type: None,
            workspace_id: None,
        };
        match client.create_session(request).await {
            Ok(session) => {
                self.session_id = Some(session.id.clone());
                self.emit(UiEvent::Session { id: session.id });
            }
            Err(err) => self.error(format!("create session failed: {err}")),
        }
    }

    /// Run one turn and pump its typed SSE events to the UI until `done`/`error`.
    async fn run_turn(&mut self, mode: String, input: String) {
        let (Some(client), Some(document_id), Some(session_id)) = (
            self.client.clone(),
            self.doc_id.clone(),
            self.session_id.clone(),
        ) else {
            self.error("session not ready; turn ignored");
            return;
        };

        let task = Task::new(document_id, mode, session_id, input);
        let stream = match client.start_turn(task).await {
            Ok(stream) => stream,
            Err(err) => {
                self.error(format!("turn failed: {err}"));
                self.emit(UiEvent::TurnEnded);
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
                        terminal |= self.emit_decoded(decoded);
                    }
                }
                Err(err) => {
                    self.error(format!("turn stream error: {err}"));
                    break;
                }
            }
            if terminal {
                break;
            }
        }
        if !terminal {
            if let Some(decoded) = decoder.finish() {
                self.emit_decoded(decoded);
            }
        }
        self.emit(UiEvent::TurnEnded);
    }

    /// Emit one decoded event; returns true when it is terminal.
    fn emit_decoded(&self, decoded: Decoded) -> bool {
        match decoded {
            Decoded::Event(event) => {
                let terminal = event.is_terminal();
                self.emit(UiEvent::Turn(event));
                terminal
            }
            Decoded::Unknown { name } => {
                self.emit(UiEvent::ProtocolNote(format!(
                    "unknown SSE event `{name}` labeled and skipped"
                )));
                false
            }
            Decoded::Invalid { name, error } => {
                self.emit(UiEvent::ProtocolNote(format!(
                    "invalid SSE `{name}` event labeled and skipped: {error}"
                )));
                false
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
