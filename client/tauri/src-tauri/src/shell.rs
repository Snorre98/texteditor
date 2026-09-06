//! The Tauri shell (F8, ADR-0013 §2): Tauri 2 Rust core + system WebView. The
//! core spawns the bundled engine as a sidecar on launch (E2, ADR-0021 §1),
//! hands the discovered base URL to the webview, and stops the engine with
//! SIGTERM → SIGKILL on quit. Native file dialogs (E7) are the capability
//! adapter's Tauri branch, registered here alongside the handshake command.
//!
//! Compiled only with the `tauri` feature; `crate::run()` is the shell entry
//! point (called from `main.rs`).

use std::sync::{Arc, Mutex};
use std::time::Duration;

use tauri::Manager;

use crate::capability::{native_pick_directory, native_pick_file};
use crate::sidecar::{spawn_engine, EngineHandle, EngineOptions, DEFAULT_STOP_GRACE};

/// Native directory picker (E7 Tauri branch, ADR-0014). Returns the chosen
/// absolute path, or `null` on cancel — the frontend then feeds it to the
/// engine (`GET /directories`, ADR-0035).
#[tauri::command]
async fn pick_directory() -> Option<String> {
    native_pick_directory().await
}

/// Native file picker (E7 Tauri branch). Returns the chosen absolute path, or
/// `null` on cancel — the frontend feeds it to `POST /documents` (ADR-0013 §3:
/// open, not read: content still flows through the engine).
#[tauri::command]
async fn pick_file() -> Option<String> {
    native_pick_file().await
}

/// The spawned sidecar. `None` until the launch-time spawn/discovery completes;
/// the `Result` carries the discovery outcome so `get_engine_base_url` can
/// surface a spawn failure (e.g. the control daemon is down, ADR-0025) instead
/// of hanging the UI. The `Arc` makes the slot cheaply cloneable so the async
/// spawn task can own a handle that outlives the `setup` closure.
#[derive(Clone, Default)]
struct EngineState(Arc<Mutex<Option<Result<EngineHandle, String>>>>);

/// The launch-time handshake's client half (E2, ADR-0021 §1): hand the
/// sidecar-discovered base URL to the webview. Polls until the spawn task
/// resolves, then returns the URL or the spawn error.
#[tauri::command]
async fn get_engine_base_url(state: tauri::State<'_, EngineState>) -> Result<String, String> {
    let deadline = std::time::Instant::now() + Duration::from_secs(20);
    loop {
        {
            let guard = state.0.lock().unwrap();
            if let Some(slot) = guard.as_ref() {
                return match slot {
                    Ok(handle) => Ok(handle.base_url().to_string()),
                    Err(e) => Err(e.clone()),
                };
            }
        }
        if std::time::Instant::now() > deadline {
            return Err("engine sidecar did not start in time".to_string());
        }
        tokio::time::sleep(Duration::from_millis(50)).await;
    }
}

/// Locate the bundled engine binary. Production: next to the app executable.
/// Dev: the Tauri CLI resolves `bundle.externalBin` under `src-tauri/binaries/`.
fn sidecar_binary() -> Option<std::path::PathBuf> {
    let name = format!(
        "texteditor-{}",
        tauri::utils::platform::target_triple().ok()?
    );
    if let Ok(exe) = std::env::current_exe() {
        if let Some(dir) = exe.parent() {
            let candidate = dir.join(&name);
            if candidate.exists() {
                return Some(candidate);
            }
        }
    }
    let dev = std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("binaries")
        .join(&name);
    dev.exists().then_some(dev)
}

/// Build and run the Tauri shell. Entry point called from `main.rs`.
pub fn run() {
    let app = tauri::Builder::default()
        .manage(EngineState::default())
        .invoke_handler(tauri::generate_handler![
            get_engine_base_url,
            pick_directory,
            pick_file
        ])
        .setup(|app| {
            // Spawn the engine sidecar on launch (E2, ADR-0021 §1). Discovery
            // runs async; `get_engine_base_url` blocks the webview until it
            // resolves, so the UI never hardcodes a port.
            let state = app.state::<EngineState>();
            match sidecar_binary() {
                Some(bin) => {
                    let slot = state.0.clone();
                    tauri::async_runtime::spawn(async move {
                        let result = spawn_engine(&EngineOptions::new(bin))
                            .await
                            .map_err(|e| e.to_string());
                        *slot.lock().unwrap() = Some(result);
                    });
                }
                None => {
                    *state.0.lock().unwrap() =
                        Some(Err("bundled engine sidecar binary not found".to_string()));
                }
            }
            Ok(())
        })
        .build(tauri::generate_context!())
        .expect("error while building tauri application");

    app.run(|app_handle, event| {
        if let tauri::RunEvent::Exit = event {
            // Quit → SIGTERM, then SIGKILL on timeout (E2, ADR-0021 §1).
            let handle = {
                let state = app_handle.state::<EngineState>();
                let taken = state.0.lock().unwrap().take();
                taken
            };
            if let Some(Ok(mut handle)) = handle {
                tauri::async_runtime::block_on(async move {
                    let _ = handle.stop(DEFAULT_STOP_GRACE).await;
                });
            }
        }
    });
}
