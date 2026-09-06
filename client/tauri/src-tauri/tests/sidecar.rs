//! End-to-end sidecar handshake tests (E2, ADR-0021 §1).
//!
//! These spawn the real engine binary and verify the launch-time handshake:
//! dynamic-port discovery via the startup log line + `/health` → `baseUrl`, and
//! the SIGTERM → SIGKILL stop contract. The engine requires the control daemon
//! (`macos-dev-config` `fleetdaemon` on `:9300`) to start, so the spawn test
//! skips (with a message) when the daemon is unreachable.
//!
//! The engine binary is located via `TEXTEDITOR_ENGINE_BIN`, else built from
//! `server/` with `go build` (the repo's Go prerequisite). The SIGKILL
//! escalation test needs neither the engine nor the daemon.

use std::net::{SocketAddr, TcpStream};
use std::path::{Path, PathBuf};
use std::process::Command as StdCommand;
use std::process::Stdio;
use std::time::Duration;

use texteditor_tauri_lib::sidecar::{
    spawn_engine, EngineHandle, EngineOptions, DEFAULT_STOP_GRACE,
};

fn engine_binary() -> Option<PathBuf> {
    if let Ok(b) = std::env::var("TEXTEDITOR_ENGINE_BIN") {
        if !b.is_empty() && Path::new(&b).exists() {
            return Some(PathBuf::from(b));
        }
    }
    let server = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../../server");
    let out = std::env::temp_dir().join(format!(
        "texteditor-sidecar-test-{}",
        std::process::id()
    ));
    std::fs::create_dir_all(&out).ok()?;
    let bin = out.join("texteditor");
    let status = StdCommand::new("go")
        .arg("build")
        .arg("-o")
        .arg(&bin)
        .arg("./cmd/texteditor")
        .current_dir(&server)
        .env("CGO_ENABLED", "0")
        .status()
        .ok()?;
    if !status.success() {
        eprintln!("SKIP: `go build` of the engine failed");
        return None;
    }
    Some(bin)
}

fn daemon_up() -> bool {
    let addr: SocketAddr = "127.0.0.1:9300".parse().unwrap();
    TcpStream::connect_timeout(&addr, Duration::from_millis(300)).is_ok()
}

#[tokio::test]
async fn spawn_discovers_dynamic_base_url_and_stops_cleanly() {
    let Some(bin) = engine_binary() else {
        eprintln!("SKIP: no engine binary (set TEXTEDITOR_ENGINE_BIN or install Go)");
        return;
    };
    if !daemon_up() {
        eprintln!("SKIP: control daemon unreachable at :9300 (start macos-dev-config/cmd/fleetdaemon)");
        return;
    }

    let data = std::env::temp_dir().join(format!(
        "texteditor-sidecar-data-{}",
        std::process::id()
    ));
    let mut handle = spawn_engine(&EngineOptions {
        data_dir: Some(data.clone()),
        ..EngineOptions::new(bin)
    })
    .await
    .expect("engine should spawn and advertise a base URL");

    let url = handle.base_url().to_string();
    assert!(
        url.starts_with("http://127.0.0.1:"),
        "unexpected base url: {url}"
    );
    let port: u16 = url.rsplit(':').next().unwrap().parse().unwrap();
    assert_ne!(port, 9100, "expected a dynamic (non-default) port");

    // `/health` is the advertised source of truth and must agree with the
    // bootstrapped URL — this also exercises the generated client end to end.
    let health = texteditor_tauri_lib::generated::HttpClient::new()
        .with_base_url(&url)
        .get_health()
        .await
        .expect("engine /health should answer");
    assert_eq!(health.base_url.as_deref(), Some(url.as_str()));
    assert_eq!(health.status.as_str(), "ok");

    // SIGTERM → clean exit, no SIGKILL escalation.
    let graceful = handle.stop(DEFAULT_STOP_GRACE).await.expect("stop");
    assert!(graceful, "engine should exit cleanly on SIGTERM");

    let _ = std::fs::remove_dir_all(data);
}

#[tokio::test]
async fn stop_escalates_to_sigkill_when_sigterm_is_ignored() {
    // A process that traps SIGTERM and loops forever: a graceful stop must
    // escalate to SIGKILL and return `false`. The `ready` marker file guarantees
    // the trap is installed before we signal (a bare sleep would race the trap).
    let ready = std::env::temp_dir().join(format!(
        "texteditor-sidecar-sh-ready-{}",
        std::process::id()
    ));
    let _ = std::fs::remove_file(&ready);
    let script = format!(
        "trap '' TERM; : > '{}'; while :; do sleep 1; done",
        ready.display()
    );
    let child = tokio::process::Command::new("sh")
        .arg("-c")
        .arg(&script)
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .kill_on_drop(false)
        .spawn()
        .expect("spawn sh");

    while !ready.exists() {
        tokio::time::sleep(Duration::from_millis(20)).await;
    }

    let mut handle = EngineHandle::from_child(child, "http://127.0.0.1:0");
    let graceful = handle
        .stop(Duration::from_millis(500))
        .await
        .expect("stop");
    assert!(!graceful, "a SIGTERM-ignoring process must require SIGKILL");

    let _ = std::fs::remove_file(&ready);
}
