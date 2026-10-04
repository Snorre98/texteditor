//! texteditor-tui-rs — standalone Ratatui TUI client (ADR-0046).
//!
//! Usage: `texteditor-tui-rs <path-to-markdown>`. The engine is resolved from
//! `ENGINE_URL` > `ENGINE_PORT` > `http://127.0.0.1:9100` and verified with a
//! `/health` probe (ADR-0021 §1).

use texteditor_tui_rs::bridge::{self, Command};
use texteditor_tui_rs::discovery::EngineEnv;
use texteditor_tui_rs::state::AppState;
use texteditor_tui_rs::ui;

fn main() {
    let Some(path) = std::env::args().nth(1) else {
        eprintln!("usage: texteditor-tui-rs <path-to-markdown>");
        eprintln!(
            "  ENGINE_URL / ENGINE_PORT select the engine (default {})",
            texteditor_tui_rs::discovery::DEFAULT_BASE_URL
        );
        std::process::exit(2);
    };

    if let Err(err) = run(path) {
        eprintln!("error: {err}");
        std::process::exit(1);
    }
}

fn run(path: String) -> std::io::Result<()> {
    ui::install_panic_hook();
    let mut terminal = ui::init()?;

    let bridge = bridge::spawn(EngineEnv::from_process_env());
    bridge.send(Command::Bootstrap { path });

    let mut app = AppState::default();
    let result = ui::run_loop(&mut terminal, &mut app, &bridge);

    ui::restore()?;
    bridge.shutdown();
    result
}
