//! texteditor-tui-rs — the standalone Ratatui TUI client for the writing-assistant
//! engine (ADR-0046).
//!
//! A **dumb client** (ADR-0013 §3): the whole API surface is generated from
//! `api/openapi.yaml` (openapi-to-rust, mounted as [`gen`]), the `/turn` SSE
//! stream is hand-decoded per ADR-0031 ([`sse`]), and every request routes to the
//! engine. The UI thread is synchronous and render-only; a tokio worker thread
//! ([`bridge`]) owns the async generated calls and the byte stream and feeds the
//! UI typed events over a channel.
//!
//! Layout:
//! - [`gen`] — committed generated client/types (never hand-shaped).
//! - [`discovery`] — `ENGINE_URL`/`ENGINE_PORT`/default resolution + `/health` probe.
//! - [`sse`] — the `/turn` SSE framing + typed per-event dispatch.
//! - [`feed`] — the `/events` non-turn liveness feed decoder (shares [`sse`]'s framing).
//! - [`state`] — the render-only snapshot and its pure event reduction.
//! - [`bridge`] — the tokio worker thread and the UI⇄engine channel shape.
//! - [`ui`] — the Ratatui widgets and the synchronous crossterm event/render loop.

/// The committed generated client tree (openapi-to-rust, `module_name = "gen"`).
/// The file lives at `src/generated/mod.rs`; mount it under the name the codegen
/// config declares. Generated code is never hand-shaped (ADR-0002/0017), so its
/// lint profile is not ours to satisfy.
#[allow(clippy::all)]
#[path = "generated/mod.rs"]
pub mod gen;

pub mod bridge;
pub mod discovery;
pub mod feed;
pub mod sse;
pub mod state;
pub mod ui;
