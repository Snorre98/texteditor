// The Tauri shell binary (F8, ADR-0013 §2). The shell logic lives in the lib
// crate (`texteditor_tauri_lib::run`) so `generate_handler!` shares a crate
// with the `#[command]` functions; this thin binary just launches it.
#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

fn main() {
    texteditor_tauri_lib::run();
}
