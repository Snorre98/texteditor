//! texteditor-tauri — the Tauri editor client for the writing-assistant engine.
//!
//! A **dumb client** (ADR-0013 §3): the whole API surface is generated from
//! `api/openapi.yaml` ([`generated`]), and native file/OS reach lives behind the
//! capability seam ([`capability`]) while all edits + versioning go through the
//! engine. The engine binary is bundled and spawned as a **sidecar** on launch
//! with dynamic-port discovery ([`sidecar`], ADR-0021 §1).
//!
//! The [`run`] entry point (the Tauri shell, F8) is behind the `tauri` feature
//! so the generated client + sidecar stay testable with no WebView dependency.

pub mod capability;
pub mod generated;
pub mod sidecar;
#[cfg(feature = "tauri")]
pub mod shell;

pub use generated::*;

/// Run the Tauri shell (F8): spawn the engine sidecar, hand the discovered base
/// URL to the webview, and stop the engine on quit. Only compiled with the
/// `tauri` feature.
#[cfg(feature = "tauri")]
pub fn run() {
    shell::run();
}
