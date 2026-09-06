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
//! `invoke` IPC (`pick_directory`/`pick_file`). The **web target** branch lives
//! in the frontend (`src/capability/web.ts`) and uses the Web File System Access
//! API (`showDirectoryPicker`/`showOpenFilePicker`) — no Rust involved. Both
//! implement the one `CapabilityAdapter` interface the Vue UI is written against
//! (ADR-0014's frontend-swap guarantee).
//!
//! The Tauri commands are gated behind the `tauri` feature so the generated
//! client + sidecar stay testable with no WebView/dialog dependency.

#[cfg(feature = "tauri")]
pub mod commands {
    use tauri::command;

    /// Open a native directory picker and return the chosen absolute path.
    /// The frontend calls this via `invoke("pick_directory")`, then feeds the
    /// path to `GET /directories` (ADR-0035).
    #[command]
    pub async fn pick_directory() -> Option<String> {
        rfd::AsyncFileDialog::new()
            .pick_folder()
            .await
            .map(|p| p.path().to_string_lossy().into_owned())
    }

    /// Open a native file picker and return the chosen absolute path. The
    /// frontend calls this via `invoke("pick_file")`, then feeds the path to
    /// `POST /documents` (ADR-0013 §3 — open, not read: content still flows
    /// through the engine).
    #[command]
    pub async fn pick_file() -> Option<String> {
        rfd::AsyncFileDialog::new()
            .pick_file()
            .await
            .map(|p| p.path().to_string_lossy().into_owned())
    }
}
