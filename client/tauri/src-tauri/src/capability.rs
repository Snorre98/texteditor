//! Capability adapter (E7, ADR-0014) — the single per-target seam.
//!
//! The one thing that differs per deployment target is how the UI reaches the
//! filesystem/OS. All app logic stays in the engine; this module exposes only
//! native *dialogs* (open/save) in the Rust core, returning absolute paths that
//! the frontend then hands to the engine (`POST /documents {path}`,
//! `GET /directories?path=`). All edits + versioning go through the engine
//! (ADR-0013 §3).
//!
//! The Rust side here backs the **Tauri target** branch of the adapter via
//! `invoke` IPC. The thin `#[tauri::command]` wrappers live in [`crate::shell`]
//! (same module as `generate_handler!`, which Tauri requires for command
//! macros); they delegate to the native dialog functions below. The **web
//! target** branch lives in the frontend (`src/capability/web.ts`) and uses the
//! Web File System Access API (`showDirectoryPicker`/`showOpenFilePicker`) — no
//! Rust involved. Both implement the one `CapabilityAdapter` interface the Vue
//! UI is written against (ADR-0014's frontend-swap guarantee).
//!
//! The dialog functions are gated behind the `tauri` feature (which also gates
//! `rfd`) so the generated client + sidecar stay testable with no dialog dep.

#[cfg(feature = "tauri")]
pub async fn native_pick_directory() -> Option<String> {
    rfd::AsyncFileDialog::new()
        .pick_folder()
        .await
        .map(|p| p.path().to_string_lossy().into_owned())
}

#[cfg(feature = "tauri")]
pub async fn native_pick_file() -> Option<String> {
    rfd::AsyncFileDialog::new()
        .pick_file()
        .await
        .map(|p| p.path().to_string_lossy().into_owned())
}
