//! Engine sidecar (E2, ADR-0021 §1) — spawn the bundled engine binary as a
//! child process, discover its base URL, and stop it cleanly.
//!
//! The launch-time handshake ADR-0021 §1 pins:
//!
//! - **Spawn** — the engine is bundled and spawned as a child on app launch
//!   (fully self-contained, not installed system-wide).
//! - **Port** — dynamic by default (`-port 0` → the engine picks a free port).
//!   The chosen base URL is bootstrapped from the engine's startup log line
//!   (`texteditor listening on http://…` — emitted to stderr by Go's `log`), then
//!   confirmed against `GET /health` → `baseUrl`, which is the advertised source
//!   of truth. Fixed mode (`ENGINE_PORT`/`-port <n>`) is preserved.
//! - **Stop** — SIGTERM, then SIGKILL on timeout (the engine already exits
//!   cleanly on SIGTERM; the Rust core escalates).
//!
//! This module is plain `tokio` + `libc` and does not depend on Tauri, so the
//! spawn/discovery/stop handshake is testable headlessly against the real engine
//! binary (see `tests/sidecar.rs`).

use std::path::PathBuf;
use std::process::Stdio;
use std::time::Duration;

use tokio::io::{AsyncBufReadExt, BufReader};
use tokio::process::{Child, Command};
use tokio::time::timeout;

pub const DEFAULT_BIND: &str = "127.0.0.1";
pub const DEFAULT_DISCOVER_TIMEOUT: Duration = Duration::from_secs(15);
/// Matches the engine's own `httpSrv.Shutdown` timeout (`cmd/texteditor/main.go`)
/// so a graceful SIGTERM has room to drain before the SIGKILL escalation.
pub const DEFAULT_STOP_GRACE: Duration = Duration::from_secs(5);

/// The `texteditor listening on http://…` marker the engine logs to stderr.
const LISTENING_MARKER: &str = "listening on ";

#[derive(Debug, thiserror::Error)]
pub enum EngineError {
    #[error("failed to spawn engine binary {0}: {1}")]
    Spawn(String, String),
    #[error("engine reported no bound base URL within {0:?}")]
    DiscoveryTimeout(Duration),
    #[error("engine exited before reporting a base URL")]
    ExitedBeforeReady,
    #[error("engine child handle already reaped")]
    NotSpawned,
}

#[derive(Debug, Clone)]
pub struct EngineOptions {
    /// Absolute path to the engine binary (bundled sidecar).
    pub binary: PathBuf,
    /// Bind address; `127.0.0.1` is the privacy default (ADR-0021 §2).
    pub bind: String,
    /// Fixed port (`Some`) or dynamic (`None` → the engine picks a free port).
    pub port: Option<u16>,
    /// Optional engine data directory (SQLite + git worktrees).
    pub data_dir: Option<PathBuf>,
    /// Optional control-daemon base URL (defaults to the engine's own).
    pub daemon_url: Option<String>,
    /// How long to wait for the startup base-URL advertisement.
    pub discover_timeout: Duration,
    /// How long to allow a graceful SIGTERM stop before escalating to SIGKILL.
    pub stop_grace: Duration,
}

impl EngineOptions {
    pub fn new(binary: impl Into<PathBuf>) -> Self {
        Self {
            binary: binary.into(),
            bind: DEFAULT_BIND.to_string(),
            port: None,
            data_dir: None,
            daemon_url: None,
            discover_timeout: DEFAULT_DISCOVER_TIMEOUT,
            stop_grace: DEFAULT_STOP_GRACE,
        }
    }
}

/// A spawned engine plus its discovered base URL.
pub struct EngineHandle {
    child: Child,
    base_url: String,
}

impl EngineHandle {
    /// The engine's advertised base URL (from `/health` → `baseUrl`).
    pub fn base_url(&self) -> &str {
        &self.base_url
    }

    /// Wrap an already-spawned child (used by tests and by the Tauri setup path
    /// when the child is spawned elsewhere).
    pub fn from_child(child: Child, base_url: impl Into<String>) -> Self {
        Self {
            child,
            base_url: base_url.into(),
        }
    }

    /// Stop the engine: SIGTERM, then SIGKILL after `grace` (ADR-0021 §1).
    /// Returns `true` when it exited on SIGTERM, `false` when SIGKILL was needed.
    pub async fn stop(&mut self, grace: Duration) -> Result<bool, EngineError> {
        terminate(&mut self.child, grace).await
    }
}

impl Drop for EngineHandle {
    /// Last-resort net: a handle dropped without `stop` gets a SIGTERM so a
    /// leaked engine can still shut itself down (the engine exits cleanly on
    /// SIGTERM). The graceful path is [`EngineHandle::stop`].
    fn drop(&mut self) {
        if let Some(pid) = self.child.id() {
            unsafe { libc::kill(pid as libc::pid_t, libc::SIGTERM) };
        }
    }
}

/// Spawn the engine and wait for it to advertise (and confirm) its base URL.
pub async fn spawn_engine(opts: &EngineOptions) -> Result<EngineHandle, EngineError> {
    let mut cmd = Command::new(&opts.binary);
    cmd.arg("-bind").arg(&opts.bind).arg("-port").arg(
        opts.port
            .map(|p| p.to_string())
            .unwrap_or_else(|| "0".to_string()),
    );
    if let Some(data) = &opts.data_dir {
        cmd.arg("-data").arg(data);
    }
    if let Some(daemon) = &opts.daemon_url {
        cmd.arg("-daemon").arg(daemon);
    }
    // The engine logs to stderr (Go `log`); stdout is unused. We own the stop
    // signal, so do not kill-on-drop at the tokio layer.
    cmd.stdin(Stdio::null());
    cmd.stdout(Stdio::null());
    cmd.stderr(Stdio::piped());
    cmd.kill_on_drop(false);

    let mut child = cmd
        .spawn()
        .map_err(|e| EngineError::Spawn(opts.binary.display().to_string(), e.to_string()))?;
    let stderr = child.stderr.take().ok_or(EngineError::ExitedBeforeReady)?;
    let mut lines = BufReader::new(stderr).lines();

    let bootstrapped = timeout(opts.discover_timeout, async {
        loop {
            match lines.next_line().await {
                Ok(Some(line)) => {
                    if let Some(url) = parse_listening_line(&line) {
                        return Some(url);
                    }
                }
                Ok(None) => return None,
                Err(_) => return None,
            }
        }
    })
    .await
    .map_err(|_| EngineError::DiscoveryTimeout(opts.discover_timeout))?
    .ok_or(EngineError::ExitedBeforeReady)?;

    // Drain the remainder of stderr so the child never blocks on a full pipe.
    tokio::spawn(async move {
        let mut lines = lines;
        while let Ok(_) = lines.next_line().await {}
    });

    // `/health` is the advertised source of truth (ADR-0021 §1): adopt its
    // `baseUrl` when present, else keep the bootstrapped URL.
    let base_url = confirm_base_url(&bootstrapped).await.unwrap_or(bootstrapped);

    Ok(EngineHandle { child, base_url })
}

/// Confirm the bootstrapped URL against `GET /health` and adopt the advertised
/// `baseUrl` when the probe succeeds.
async fn confirm_base_url(bootstrapped: &str) -> Option<String> {
    let health = crate::generated::HttpClient::new()
        .with_base_url(bootstrapped)
        .get_health()
        .await
        .ok()?;
    health
        .base_url
        .filter(|u| !u.is_empty())
        .or_else(|| Some(bootstrapped.to_string()))
}

/// Send SIGTERM, wait up to `grace`, then escalate to SIGKILL. Returns `true` on
/// a graceful SIGTERM exit, `false` when the escalation fired.
pub(crate) async fn terminate(
    child: &mut Child,
    grace: Duration,
) -> Result<bool, EngineError> {
    let pid = child.id().ok_or(EngineError::NotSpawned)? as libc::pid_t;
    // SAFETY: pid is the spawned child's own pid; these are standard POSIX signals.
    unsafe { libc::kill(pid, libc::SIGTERM) };
    match timeout(grace, child.wait()).await {
        Ok(_) => Ok(true),
        Err(_) => {
            unsafe { libc::kill(pid, libc::SIGKILL) };
            let _ = child.wait().await;
            Ok(false)
        }
    }
}

fn parse_listening_line(line: &str) -> Option<String> {
    let idx = line.find(LISTENING_MARKER)?;
    let rest = &line[idx + LISTENING_MARKER.len()..];
    let url = rest.split_whitespace().next()?;
    (url.starts_with("http://") || url.starts_with("https://")).then(|| url.to_string())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parses_listening_line() {
        assert_eq!(
            parse_listening_line(
                "2026/09/06 16:06:20 texteditor listening on http://127.0.0.1:65403 (daemon http://127.0.0.1:9300)"
            ),
            Some("http://127.0.0.1:65403".to_string())
        );
    }

    #[test]
    fn ignores_non_listening_lines() {
        assert_eq!(parse_listening_line("warning: binding 0.0.0.0 exposes…"), None);
        assert_eq!(parse_listening_line(""), None);
    }
}
