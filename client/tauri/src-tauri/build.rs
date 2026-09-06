fn main() {
    // The Tauri build script only runs when the `tauri` feature is on, so the
    // generated client + sidecar (`cargo test`, default features) never depend
    // on the Tauri config or a frontend build.
    if std::env::var("CARGO_FEATURE_TAURI").is_ok() {
        tauri_build::build();
    }
}
