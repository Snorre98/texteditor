//! The async bridge (ADR-0046 §4, ADR-0052 §5).
//!
//! One tokio runtime on a worker OS thread owns the generated async calls, the
//! `/turn` byte stream, and the long-lived `/events` liveness feed. The UI
//! thread stays synchronous and render-only and exchanges messages with the
//! worker over channels:
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
//! name and the typed message per turn. Selection, sequencing, and liveness are
//! engine-owned (ADR-0052): bootstrap is one `POST /open`, session resume is one
//! `POST /documents/{id}/session`, approve is one atomic `accept`, and non-turn
//! state arrives on `GET /events`. No domain logic runs here — it routes bytes
//! and events.

use std::sync::mpsc::{self, Receiver, Sender};
use std::thread;

use futures_util::StreamExt;

use crate::discovery::{self, EngineEnv};
use crate::feed::{FeedDecoded, FeedDecoder, FeedEvent};
use crate::gen::{
    AcceptBlockApiError, CommitRequest, ContextPolicy, CorpusIndexRequest, CreateSessionRequest,
    EvictCorpusDocumentApiError, FleetEvent, FleetEventControl, FleetState, FleetStateControl,
    HttpClient, ListDirectoryApiError, OpenApiError, OpenRequest, OpenResult, OpenResultKind,
    PutCorpusApiError, PutCorpusRequest, RenameSessionRequest, Session, Task,
};
use crate::sse::{Decoded, SseDecoder};
use crate::state::{FleetErrorInfo, UiEvent};

/// A request from the UI thread to the worker.
#[derive(Debug, Clone)]
pub enum Command {
    /// Engine-owned bootstrap: resolve `path` into a directory listing or a
    /// document context in one `POST /open` (ADR-0052 §1).
    Open { path: String },
    /// Open a document chosen from the directory picker (same `POST /open`).
    OpenDocument { path: String },
    /// List one directory (bounded by ALLOWED_ROOTS).
    ListDirectory { path: String },
    /// Refresh the workspace-scoped session list (the picker overlay; ADR-0049).
    ListSessions,
    /// Resume an existing session chosen from the list.
    OpenSession { session: Session },
    /// Create a new doc-level session for the active document.
    NewSession,
    /// Rename a session.
    RenameSession { session_id: String, title: String },
    /// Run one turn with the active preset; `context` is the optional per-turn
    /// override (ADR-0049 §8) and `mentions` the turn-scoped attachments.
    SendTurn {
        mode: String,
        input: String,
        context: Option<ContextPolicy>,
        mentions: Vec<String>,
    },
    /// Cancel the running turn by id (POST /turns/{id}/cancel).
    Cancel { turn_id: String },
    /// Answer a waiting turn's `/locate` ambiguity picker (ADR-0048 §4).
    ResolveLocate {
        turn_id: String,
        chunk_key: Option<String>,
        cancel: bool,
    },
    /// Load the active document's block tree (reader; ADR-0050).
    LoadBlocks,
    /// Persist the session context policy (tray; ADR-0049 §8).
    PutSessionContext { policy: ContextPolicy },
    /// Read the workspace's corpus scope + per-document status + job progress.
    GetCorpus,
    /// Set the corpus scope (idempotent, index-only; ADR-0049 §3/§4).
    PutCorpus {
        roots: Vec<String>,
        include: Vec<String>,
        exclude: Vec<String>,
    },
    /// Bulk (re)index the current scope.
    IndexCorpus,
    /// Evict one corpus document by its path-derived id.
    EvictCorpusDocument { id: String },
    /// Read the fleet observability state (`GET /fleet`) (E3).
    GetFleet,
    /// Start a model (raw lifecycle verb; the engine orchestrates).
    StartModel { name: String },
    /// Stop a model (raw lifecycle verb).
    StopModel { name: String },
    /// Provision a model (raw lifecycle verb).
    ProvisionModel { name: String },
    /// Atomically accept the staged candidate for `block_id` (ADR-0052 §3).
    /// `overwrite` is the explicit external-change opt-in.
    Accept { block_id: String, overwrite: bool },
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
        feed_task: None,
        feed_workspace: None,
    };

    // The running turn's task handle (at most one turn at a time — the UI
    // guards this). Kept so Shutdown can abort an in-flight stream.
    let mut active: Option<tokio::task::JoinHandle<()>> = None;

    while let Some(command) = cmd_rx.recv().await {
        match command {
            Command::Open { path } | Command::OpenDocument { path } => worker.open(path).await,
            Command::ListDirectory { path } => worker.list_directory(path).await,
            Command::ListSessions => worker.refresh_sessions().await,
            Command::OpenSession { session } => worker.open_session(session),
            Command::NewSession => worker.new_session().await,
            Command::RenameSession { session_id, title } => {
                worker.rename_session(session_id, title).await
            }
            Command::SendTurn {
                mode,
                input,
                context,
                mentions,
            } => match worker.build_turn_task(mode, input, context, mentions) {
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
            Command::ResolveLocate {
                turn_id,
                chunk_key,
                cancel,
            } => worker.resolve_locate(turn_id, chunk_key, cancel).await,
            Command::LoadBlocks => worker.load_blocks().await,
            Command::PutSessionContext { policy } => worker.put_session_context(policy).await,
            Command::GetCorpus => worker.refresh_corpus().await,
            Command::PutCorpus {
                roots,
                include,
                exclude,
            } => worker.put_corpus(roots, include, exclude).await,
            Command::IndexCorpus => worker.index_corpus().await,
            Command::EvictCorpusDocument { id } => worker.evict_corpus_document(id).await,
            Command::GetFleet => worker.refresh_fleet().await,
            Command::StartModel { name } => worker.start_model(name).await,
            Command::StopModel { name } => worker.stop_model(name).await,
            Command::ProvisionModel { name } => worker.provision_model(name).await,
            Command::Accept {
                block_id,
                overwrite,
            } => worker.accept(block_id, overwrite).await,
            Command::Shutdown => {
                if let Some(handle) = active.take() {
                    handle.abort();
                }
                if let Some(handle) = worker.feed_task.take() {
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
    /// The long-lived `/events` subscription task (aborted/replaced on workspace
    /// change, ADR-0052 §4).
    feed_task: Option<tokio::task::JoinHandle<()>>,
    /// The workspace the current feed subscription is scoped to.
    feed_workspace: Option<String>,
}

impl Worker {
    fn emit(&self, event: UiEvent) {
        let _ = self.event_tx.send(event);
    }

    fn error(&self, message: impl Into<String>) {
        self.emit(UiEvent::Error(message.into()));
    }

    /// Engine-owned bootstrap (ADR-0052 §1): one `POST /open` resolves the
    /// workspace plus either a directory listing or a document context.
    async fn open(&mut self, path: String) {
        let Some(client) = self.client.clone() else {
            self.error("engine not connected; cannot open");
            return;
        };
        let request = OpenRequest {
            path,
            anchor_block_id: None,
            mode_type: None,
        };
        match client.open(request).await {
            Ok(result) => self.handle_open_result(result).await,
            Err(err) => {
                if let Some(api) = err.api() {
                    match &api.typed {
                        Some(OpenApiError::Status403(refusal)) => {
                            self.emit(UiEvent::DirectoryRefused {
                                path: refusal.path.clone(),
                                allowed_roots: refusal.allowed_roots.clone(),
                            });
                            return;
                        }
                        Some(OpenApiError::Status404(_)) => {
                            self.error("open: path not found");
                            return;
                        }
                        None => {}
                    }
                }
                self.error(format!("open failed: {err}"));
            }
        }
    }

    /// Render one `POST /open` result and (re)subscribe the feed to its
    /// workspace. The client performs no path arithmetic or multi-call probing.
    async fn handle_open_result(&mut self, result: OpenResult) {
        let workspace_id = result.workspace.id.clone();
        self.workspace_id = Some(workspace_id.clone());
        self.emit(UiEvent::Workspace {
            id: workspace_id.clone(),
            root: result.workspace.root.clone(),
            name: result.workspace.name.clone(),
        });

        match result.kind {
            OpenResultKind::Directory => {
                self.doc_id = None;
                self.session_id = None;
                self.emit(UiEvent::Blocks(Vec::new()));
                if let Some(listing) = result.listing {
                    self.emit(UiEvent::Directory { listing });
                }
            }
            OpenResultKind::Document => {
                if let Some(document) = result.document {
                    self.doc_id = Some(document.id.clone());
                    self.emit(UiEvent::Document {
                        id: document.id,
                        path: document.path,
                        external_change: document.external_change.unwrap_or(false),
                    });
                }
                if let Some(blocks) = result.blocks {
                    self.emit(UiEvent::Blocks(blocks));
                }
                if let Some(modes) = result.modes {
                    self.emit(UiEvent::Modes(modes));
                }
                if let Some(session) = result.session {
                    self.session_id = Some(session.id.clone());
                    self.emit(UiEvent::Session {
                        id: session.id,
                        title: session.title.clone(),
                        policy: session.context_policy.clone(),
                    });
                }
            }
        }

        self.restart_feed(workspace_id);
        self.refresh_corpus().await;
    }

    /// (Re)subscribe the `/events` feed to a workspace. A workspace change
    /// aborts the old subscription and starts a new one.
    fn restart_feed(&mut self, workspace_id: String) {
        if self.feed_workspace.as_deref() == Some(workspace_id.as_str()) && self.feed_task.is_some()
        {
            return;
        }
        if let Some(handle) = self.feed_task.take() {
            handle.abort();
        }
        let Some(client) = self.client.clone() else {
            return;
        };
        let tx = self.event_tx.clone();
        self.feed_workspace = Some(workspace_id.clone());
        self.feed_task = Some(tokio::spawn(feed_loop(client, workspace_id, tx)));
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
                    policy: session.context_policy,
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
            policy: session.context_policy,
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
                        policy: session.context_policy.clone(),
                    });
                }
                self.refresh_sessions().await;
            }
            Err(err) => self.error(format!("rename session failed: {err}")),
        }
    }

    /// Load the active document's block tree for the reader (ADR-0050 §2).
    async fn load_blocks(&self) {
        let (Some(client), Some(document_id)) = (self.client.clone(), self.doc_id.clone()) else {
            return;
        };
        match client.get_blocks(&document_id).await {
            Ok(blocks) => self.emit(UiEvent::Blocks(blocks)),
            Err(err) => self.error(format!("load document failed: {err}")),
        }
    }

    /// Persist the session context policy (tray; ADR-0049 §8).
    async fn put_session_context(&self, policy: ContextPolicy) {
        let (Some(client), Some(session_id)) = (self.client.clone(), self.session_id.clone())
        else {
            self.error("no session; tray not saved");
            return;
        };
        match client.put_session_context(&session_id, policy).await {
            Ok(session) => {
                if let Some(policy) = session.context_policy {
                    self.emit(UiEvent::SessionPolicy(policy));
                }
            }
            Err(err) => self.error(format!("save tray failed: {err}")),
        }
    }

    /// Read the workspace corpus state (ADR-0049 §4). Progress arrives on the
    /// liveness feed; this is the on-demand status read (overlay open).
    async fn refresh_corpus(&self) {
        let (Some(client), Some(workspace_id)) = (self.client.clone(), self.workspace_id.clone())
        else {
            return;
        };
        match client.get_corpus(&workspace_id).await {
            Ok(state) => self.emit(UiEvent::Corpus(state)),
            Err(err) => self.error(format!("load corpus failed: {err}")),
        }
    }

    /// Set the corpus scope (idempotent, index-only; ADR-0049 §3/§4). A root
    /// outside ALLOWED_ROOTS is the typed `path-outside-allowed-roots` refusal.
    async fn put_corpus(&self, roots: Vec<String>, include: Vec<String>, exclude: Vec<String>) {
        let (Some(client), Some(workspace_id)) = (self.client.clone(), self.workspace_id.clone())
        else {
            self.error("no workspace; corpus scope not saved");
            return;
        };
        let request = PutCorpusRequest {
            workspace_id,
            roots: Some(roots),
            include: Some(include),
            exclude: Some(exclude),
        };
        match client.put_corpus(request).await {
            Ok(state) => self.emit(UiEvent::Corpus(state)),
            Err(err) => {
                if let Some(api) = err.api() {
                    if let Some(PutCorpusApiError::Status403(refusal)) = &api.typed {
                        self.emit(UiEvent::CorpusRefused {
                            path: refusal.path.clone(),
                            allowed_roots: refusal.allowed_roots.clone(),
                        });
                        return;
                    }
                }
                self.error(format!("save corpus scope failed: {err}"));
            }
        }
    }

    /// Bulk (re)index the current scope; emit the accepted job, then refresh.
    async fn index_corpus(&self) {
        let (Some(client), Some(workspace_id)) = (self.client.clone(), self.workspace_id.clone())
        else {
            self.error("no workspace; cannot index corpus");
            return;
        };
        match client
            .index_corpus(CorpusIndexRequest { workspace_id })
            .await
        {
            Ok(job) => {
                self.emit(UiEvent::CorpusJob(job));
                self.refresh_corpus().await;
            }
            Err(err) => self.error(format!("index corpus failed: {err}")),
        }
    }

    /// Evict one corpus document (idempotent); then refresh.
    async fn evict_corpus_document(&self, id: String) {
        let (Some(client), Some(workspace_id)) = (self.client.clone(), self.workspace_id.clone())
        else {
            self.error("no workspace; cannot evict");
            return;
        };
        match client.evict_corpus_document(&id, &workspace_id).await {
            Ok(()) => self.refresh_corpus().await,
            Err(err) => {
                if let Some(api) = err.api() {
                    if let Some(EvictCorpusDocumentApiError::Status403(refusal)) = &api.typed {
                        self.emit(UiEvent::CorpusRefused {
                            path: refusal.path.clone(),
                            allowed_roots: refusal.allowed_roots.clone(),
                        });
                        return;
                    }
                }
                self.error(format!("evict corpus document failed: {err}"));
            }
        }
    }

    /// Read the fleet observability state (`GET /fleet`) (E3). Ongoing changes
    /// arrive on the liveness feed; the client refetches on open and after each
    /// raw lifecycle verb.
    async fn refresh_fleet(&self) {
        let Some(client) = self.client.clone() else {
            return;
        };
        match client.get_fleet().await {
            Ok(state) => self.emit(UiEvent::Fleet(state)),
            Err(err) => self.emit(UiEvent::FleetError(FleetErrorInfo {
                message: format!("load fleet failed: {err}"),
                provision_hint: false,
            })),
        }
    }

    /// Raw lifecycle verb: start a model, then refetch the fleet.
    async fn start_model(&self, name: String) {
        let Some(client) = self.client.clone() else {
            return;
        };
        match client.start_model(&name).await {
            Ok(_) => self.refresh_fleet().await,
            Err(err) => self.emit(UiEvent::FleetError(fleet_action_error("start", &name, err))),
        }
    }

    /// Raw lifecycle verb: stop a model, then refetch the fleet.
    async fn stop_model(&self, name: String) {
        let Some(client) = self.client.clone() else {
            return;
        };
        match client.stop_model(&name).await {
            Ok(_) => self.refresh_fleet().await,
            Err(err) => self.emit(UiEvent::FleetError(fleet_action_error("stop", &name, err))),
        }
    }

    /// Raw lifecycle verb: provision a model, then refetch the fleet.
    async fn provision_model(&self, name: String) {
        let Some(client) = self.client.clone() else {
            return;
        };
        match client.provision_model(&name).await {
            Ok(_) => self.refresh_fleet().await,
            Err(err) => self.emit(UiEvent::FleetError(fleet_action_error(
                "provision",
                &name,
                err,
            ))),
        }
    }

    /// Build the turn Task from the worker's engine facts.
    fn build_turn_task(
        &self,
        mode: String,
        input: String,
        context: Option<ContextPolicy>,
        mentions: Vec<String>,
    ) -> Option<Task> {
        let document_id = self.doc_id.clone()?;
        let session_id = self.session_id.clone()?;
        let mut task = Task::new(document_id, mode, session_id, input);
        task.workspace_id = self.workspace_id.clone();
        task.context = context;
        if !mentions.is_empty() {
            task.mentions = Some(
                mentions
                    .into_iter()
                    .map(|path| crate::gen::Mention { path })
                    .collect(),
            );
        }
        Some(task)
    }

    /// Answer a waiting `/locate` picker (ADR-0048 §4).
    async fn resolve_locate(&self, turn_id: String, chunk_key: Option<String>, cancel: bool) {
        let Some(client) = self.client.clone() else {
            return;
        };
        let choice = crate::gen::LocateChoice {
            chunk_key,
            cancel: if cancel { Some(true) } else { None },
        };
        if let Err(err) = client.resolve_locate(&turn_id, choice).await {
            self.error(format!("locate choice failed: {err}"));
        }
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
                        Some(crate::gen::CancelTurnApiError::Status409(_)) => {
                            self.error("cancel: turn is not running")
                        }
                        Some(crate::gen::CancelTurnApiError::Status404(_)) => {
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

    /// The atomic approve write boundary (ADR-0052 §3): one `accept` call
    /// commits + writes through; the client sends no candidate text. An external
    /// change is the typed 409, rendered as a labeled conflict.
    async fn accept(&mut self, block_id: String, overwrite: bool) {
        let (Some(client), Some(document_id)) = (self.client.clone(), self.doc_id.clone()) else {
            self.error("document not ready; accept ignored");
            return;
        };
        let request = CommitRequest {
            overwrite: Some(overwrite),
        };
        match client
            .accept_block(&document_id, &block_id, Some(request))
            .await
        {
            Ok(revision) => {
                self.emit(UiEvent::Approved { revision });
                // The engine's tree changed; refresh the reader (ADR-0050).
                self.load_blocks().await;
            }
            Err(err) => {
                if let Some(api) = err.api() {
                    if let Some(AcceptBlockApiError::Status409(conflict)) = &api.typed {
                        self.emit(UiEvent::Conflict {
                            message: format!("file-changed-externally: {}", conflict.path),
                        });
                        return;
                    }
                }
                self.error(format!("accept failed: {err}"));
            }
        }
    }
}

/// Build a labeled fleet lifecycle-verb error; `provision_hint` is set when the
/// failure implies the model is not provisioned (ADR-0040 §5).
fn fleet_action_error(action: &str, name: &str, err: impl std::fmt::Display) -> FleetErrorInfo {
    let rendered = err.to_string();
    let lower = rendered.to_lowercase();
    let provision_hint = lower.contains("model-not-found")
        || lower.contains("not found")
        || lower.contains("provision");
    FleetErrorInfo {
        message: format!("fleet {action} {name}: {rendered}"),
        provision_hint,
    }
}

/// Subscribe to the non-turn `/events` feed and forward typed events to the UI.
/// The subscription is scoped to a workspace (ADR-0052 §4) and reconnects with a
/// short backoff; the task is aborted on workspace change or shutdown.
async fn feed_loop(client: HttpClient, workspace_id: String, event_tx: Sender<UiEvent>) {
    loop {
        match client.get_events(Some(workspace_id.as_str())).await {
            Ok(stream) => {
                futures_util::pin_mut!(stream);
                let mut decoder = FeedDecoder::new();
                while let Some(chunk) = stream.next().await {
                    match chunk {
                        Ok(bytes) => {
                            for decoded in decoder.push(&bytes) {
                                emit_feed(&event_tx, decoded);
                            }
                        }
                        Err(err) => {
                            let _ = event_tx.send(UiEvent::ProtocolNote(format!(
                                "feed stream error: {err}"
                            )));
                            break;
                        }
                    }
                }
                if let Some(decoded) = decoder.finish() {
                    emit_feed(&event_tx, decoded);
                }
            }
            Err(err) => {
                let _ = event_tx.send(UiEvent::ProtocolNote(format!(
                    "feed subscribe failed: {err}"
                )));
            }
        }
        tokio::time::sleep(std::time::Duration::from_secs(2)).await;
    }
}

/// Emit one decoded feed event to the UI; unknown/invalid events are labeled.
fn emit_feed(event_tx: &Sender<UiEvent>, decoded: FeedDecoded) {
    match decoded {
        FeedDecoded::Event(event) => {
            let ui = match event {
                FeedEvent::Corpus(e) => UiEvent::CorpusJob(e.job),
                FeedEvent::Document(e) => UiEvent::FeedDocument(e),
                FeedEvent::Session(e) => UiEvent::FeedSession(e),
                FeedEvent::Fleet(e) => UiEvent::Fleet(fleet_state_from_event(e)),
            };
            let _ = event_tx.send(ui);
        }
        FeedDecoded::Unknown { name } => {
            let _ = event_tx.send(UiEvent::ProtocolNote(format!(
                "unknown feed event `{name}` labeled and skipped"
            )));
        }
        FeedDecoded::Invalid { name, error } => {
            let _ = event_tx.send(UiEvent::ProtocolNote(format!(
                "invalid feed `{name}` event labeled and skipped: {error}"
            )));
        }
    }
}

/// Map a feed `fleet` event onto the `/fleet` render shape (identical fields).
fn fleet_state_from_event(event: FleetEvent) -> FleetState {
    let control = match event.control {
        FleetEventControl::Up => FleetStateControl::Up,
        FleetEventControl::Unreachable => FleetStateControl::Unreachable,
    };
    FleetState {
        control,
        models: event.models,
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

#[cfg(test)]
mod tests {
    use super::{fleet_action_error, fleet_state_from_event};
    use crate::gen::{FleetEvent, FleetEventControl, FleetModel, FleetModelLiveState, FleetStateControl};

    #[test]
    fn fleet_action_error_flags_the_provision_hint() {
        let hint = fleet_action_error(
            "start",
            "phi-4",
            "500 model-not-found: name not in the fleet",
        );
        assert!(hint.provision_hint);
        assert!(hint.message.contains("start phi-4"));

        let other = fleet_action_error("stop", "phi-4", "500 daemon-unreachable");
        assert!(!other.provision_hint);
    }

    #[test]
    fn fleet_event_maps_to_the_render_shape() {
        let event = FleetEvent {
            control: FleetEventControl::Unreachable,
            models: vec![FleetModel {
                base_url: "http://x/v1".to_string(),
                capabilities: None,
                live_state: FleetModelLiveState::Unknown,
                mode_tags: None,
                name: "m".to_string(),
            }],
        };
        let state = fleet_state_from_event(event);
        assert_eq!(state.control, FleetStateControl::Unreachable);
        assert_eq!(state.models[0].name, "m");
    }
}
