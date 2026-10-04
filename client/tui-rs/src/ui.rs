//! The Ratatui widgets and the synchronous crossterm event/render loop
//! (ADR-0046 §1, §4, §6).
//!
//! The loop renders from the render-only [`AppState`] snapshot and never computes
//! domain values (ADR-0013 §3). Bracketed paste is enabled so multi-line pastes
//! insert verbatim — required by `/locate` (ADR-0048).

use std::io::{self, Stdout};
use std::path::Path;
use std::time::Duration;

use crossterm::event::{
    self, DisableBracketedPaste, EnableBracketedPaste, Event, KeyCode, KeyEvent, KeyModifiers,
};
use crossterm::execute;
use crossterm::terminal::{
    disable_raw_mode, enable_raw_mode, EnterAlternateScreen, LeaveAlternateScreen,
};
use ratatui::backend::CrosstermBackend;
use ratatui::layout::{Constraint, Direction, Layout, Position, Rect};
use ratatui::style::{Color, Modifier, Style};
use ratatui::text::{Line, Span};
use ratatui::widgets::{Block, Borders, Clear, Paragraph, Tabs, Wrap};
use ratatui::{Frame, Terminal};

use crate::bridge::{Bridge, Command};
use crate::state::{AppState, ConnectionState, Overlay, Role};

type Tui = Terminal<CrosstermBackend<Stdout>>;

/// Enter the alternate screen, enable raw mode + bracketed paste.
pub fn init() -> io::Result<Tui> {
    enable_raw_mode()?;
    let mut stdout = io::stdout();
    execute!(stdout, EnterAlternateScreen, EnableBracketedPaste)?;
    Terminal::new(CrosstermBackend::new(stdout))
}

/// Restore the terminal (raw mode off, bracketed paste off, leave alternate screen).
pub fn restore() -> io::Result<()> {
    disable_raw_mode()?;
    execute!(io::stdout(), DisableBracketedPaste, LeaveAlternateScreen)?;
    Ok(())
}

/// Restore the terminal on panic so a failure never leaves the tty raw.
pub fn install_panic_hook() {
    let default_hook = std::panic::take_hook();
    std::panic::set_hook(Box::new(move |info| {
        let _ = restore();
        default_hook(info);
    }));
}

/// Run the UI until `should_quit`. Events are drained non-blockingly from the
/// bridge and the frame is redrawn; input is polled with a short timeout.
pub fn run_loop(terminal: &mut Tui, app: &mut AppState, bridge: &Bridge) -> io::Result<()> {
    let mut scroll: u16 = 0;
    let mut follow = true;

    while !app.should_quit {
        while let Ok(event) = bridge.event_rx.try_recv() {
            app.reduce(event);
        }

        let desired = if follow { u16::MAX } else { scroll };
        let mut max_scroll = 0u16;
        terminal.draw(|frame| {
            max_scroll = render(frame, app, desired);
        })?;
        scroll = if follow {
            max_scroll
        } else {
            scroll.min(max_scroll)
        };

        if event::poll(Duration::from_millis(50))? {
            match event::read()? {
                Event::Key(key) => handle_key(key, app, bridge, &mut scroll, &mut follow),
                Event::Paste(text) => app.input.push_str(&text),
                _ => {}
            }
        }
    }
    Ok(())
}

fn handle_key(
    key: KeyEvent,
    app: &mut AppState,
    bridge: &Bridge,
    scroll: &mut u16,
    follow: &mut bool,
) {
    let ctrl = key.modifiers.contains(KeyModifiers::CONTROL);

    // Session-title editing owns the keyboard while active.
    if app.rename.is_some() {
        match key.code {
            KeyCode::Enter => {
                if let Some((session_id, _)) = selected_session(app) {
                    let title = app.rename.take().unwrap_or_default();
                    bridge.send(Command::RenameSession { session_id, title });
                } else {
                    app.rename = None;
                }
            }
            KeyCode::Esc => app.rename = None,
            KeyCode::Backspace => {
                if let Some(buf) = app.rename.as_mut() {
                    buf.pop();
                }
            }
            KeyCode::Char(ch) if !ctrl => {
                if let Some(buf) = app.rename.as_mut() {
                    buf.push(ch);
                }
            }
            _ => {}
        }
        return;
    }

    // Retrieval-query editing owns the keyboard while active.
    if app.query_edit.is_some() {
        match key.code {
            KeyCode::Enter => {
                let query = app.query_edit.take().unwrap_or_default();
                let policy = session_policy_with_query(app, Some(query));
                bridge.send(Command::PutSessionContext { policy });
            }
            KeyCode::Esc => app.query_edit = None,
            KeyCode::Backspace => {
                if let Some(buf) = app.query_edit.as_mut() {
                    buf.pop();
                }
            }
            KeyCode::Char(ch) if !ctrl => {
                if let Some(buf) = app.query_edit.as_mut() {
                    buf.push(ch);
                }
            }
            _ => {}
        }
        return;
    }

    // Modal overlays own the keyboard while open.
    match app.overlay {
        Overlay::Directory => {
            handle_directory_key(key, app, bridge);
            return;
        }
        Overlay::Sessions => {
            handle_sessions_key(key, app, bridge);
            return;
        }
        Overlay::Tray => {
            handle_tray_key(key, app, bridge);
            return;
        }
        Overlay::Mentions => {
            handle_mentions_key(key, app, bridge);
            return;
        }
        Overlay::Locate => {
            handle_locate_key(key, app, bridge);
            return;
        }
        Overlay::None => {}
    }

    match key.code {
        KeyCode::Char('c') if ctrl => app.should_quit = true,
        KeyCode::Esc => app.should_quit = true,
        KeyCode::Char('a') if ctrl => {
            if let Some(candidate) = &app.candidate {
                if !candidate.block_id.is_empty() {
                    bridge.send(Command::Approve {
                        block_id: candidate.block_id.clone(),
                    });
                }
            }
        }
        KeyCode::Char('o') if ctrl => {
            if let Some(candidate) = &app.candidate {
                if !candidate.block_id.is_empty() {
                    bridge.send(Command::Overwrite {
                        block_id: candidate.block_id.clone(),
                    });
                }
            }
        }
        KeyCode::Char('x') if ctrl => {
            if app.turn_active {
                if let Some(turn_id) = app.turn_id.clone() {
                    bridge.send(Command::Cancel { turn_id });
                } else {
                    app.error = Some("cancel: turn id not yet known".to_string());
                }
            }
        }
        KeyCode::Char('w') if ctrl => open_directory_picker(app, bridge),
        KeyCode::Char('s') if ctrl => {
            bridge.send(Command::ListSessions);
            app.overlay = Overlay::Sessions;
            app.picker_index = 0;
        }
        KeyCode::Char('r') if ctrl => app.reader_visible = !app.reader_visible,
        KeyCode::Char('i') if ctrl => app.inspector_visible = !app.inspector_visible,
        KeyCode::Char('t') if ctrl => {
            app.overlay = Overlay::Tray;
            app.tray_index = 0;
        }
        KeyCode::Enter => {
            if !app.turn_active {
                if let Some(mode) = app.active_mode.clone() {
                    let input = app.input.trim().to_string();
                    if !input.is_empty() {
                        app.begin_turn(input.clone());
                        // A held tray override rides this turn only (ADR-0049 §8).
                        let context = if app.override_next_turn {
                            app.session_policy.clone()
                        } else {
                            None
                        };
                        app.override_next_turn = false;
                        let mentions = std::mem::take(&mut app.mentions);
                        bridge.send(Command::SendTurn {
                            mode,
                            input,
                            context,
                            mentions,
                        });
                        *follow = true;
                    }
                }
            }
        }
        KeyCode::Char('@') => open_mention_picker(app, bridge),
        KeyCode::Tab => cycle_mode(app, 1),
        KeyCode::BackTab => cycle_mode(app, -1),
        KeyCode::Backspace => {
            app.input.pop();
        }
        KeyCode::Up => {
            *follow = false;
            *scroll = scroll.saturating_sub(1);
        }
        KeyCode::Down => {
            *follow = false;
            *scroll = scroll.saturating_add(1);
        }
        KeyCode::PageUp => {
            *follow = false;
            *scroll = scroll.saturating_sub(5);
        }
        KeyCode::PageDown => {
            *follow = false;
            *scroll = scroll.saturating_add(5);
        }
        KeyCode::Char(ch) if !ctrl => app.input.push(ch),
        _ => {}
    }
}

/// Open the directory picker at the workspace root (or the document parent).
fn open_directory_picker(app: &mut AppState, bridge: &Bridge) {
    let start = app.workspace.as_ref().map(|w| w.root.clone()).or_else(|| {
        app.document_path
            .as_deref()
            .and_then(|p| Path::new(p).parent())
            .map(|p| p.to_string_lossy().into_owned())
    });
    let Some(path) = start else {
        app.error = Some("no workspace or document to browse".to_string());
        return;
    };
    bridge.send(Command::ListDirectory { path });
    app.overlay = Overlay::Directory;
    app.picker_index = 0;
}

/// Directory-picker keys: navigate, go up, open a file, or close.
fn handle_directory_key(key: KeyEvent, app: &mut AppState, bridge: &Bridge) {
    let entry_count = app
        .directory
        .as_ref()
        .map(|d| d.entries.len() + 1) // +1 for the virtual ".."
        .unwrap_or(1);
    match key.code {
        KeyCode::Esc => app.overlay = Overlay::None,
        KeyCode::Up => app.picker_index = app.picker_index.saturating_sub(1),
        KeyCode::Down => {
            if app.picker_index + 1 < entry_count {
                app.picker_index += 1;
            }
        }
        KeyCode::Backspace => go_up_directory(app, bridge),
        KeyCode::Enter => {
            if app.picker_index == 0 {
                go_up_directory(app, bridge);
                return;
            }
            let Some(listing) = app.directory.as_ref() else {
                return;
            };
            let Some(entry) = listing.entries.get(app.picker_index - 1).cloned() else {
                return;
            };
            if entry.is_dir {
                bridge.send(Command::ListDirectory { path: entry.path });
                app.picker_index = 0;
            } else {
                bridge.send(Command::OpenDocument { path: entry.path });
                app.overlay = Overlay::None;
            }
        }
        _ => {}
    }
}

/// Move the picker to the listing's parent directory.
fn go_up_directory(app: &mut AppState, bridge: &Bridge) {
    let Some(listing) = app.directory.as_ref() else {
        return;
    };
    let Some(parent) = Path::new(&listing.path).parent() else {
        return;
    };
    bridge.send(Command::ListDirectory {
        path: parent.to_string_lossy().into_owned(),
    });
    app.picker_index = 0;
}

/// The selected session in the list overlay (id, title).
fn selected_session(app: &AppState) -> Option<(String, Option<String>)> {
    app.sessions
        .get(app.picker_index)
        .map(|s| (s.id.clone(), s.title.clone()))
}

/// Session-list keys: resume, create, rename, or close.
fn handle_sessions_key(key: KeyEvent, app: &mut AppState, bridge: &Bridge) {
    match key.code {
        KeyCode::Esc => app.overlay = Overlay::None,
        KeyCode::Up => app.picker_index = app.picker_index.saturating_sub(1),
        KeyCode::Down => {
            if app.picker_index + 1 < app.sessions.len() {
                app.picker_index += 1;
            }
        }
        KeyCode::Char('n') => {
            bridge.send(Command::NewSession);
            app.overlay = Overlay::None;
        }
        KeyCode::Char('r') => {
            if let Some((_, title)) = selected_session(app) {
                app.rename = Some(title.unwrap_or_default());
            }
        }
        KeyCode::Enter => {
            if let Some(session) = app.sessions.get(app.picker_index).cloned() {
                bridge.send(Command::OpenSession { session });
                app.overlay = Overlay::None;
            }
        }
        _ => {}
    }
}

/// Open the `@`-mention picker at the workspace root (or the document parent).
fn open_mention_picker(app: &mut AppState, bridge: &Bridge) {
    let start = app.workspace.as_ref().map(|w| w.root.clone()).or_else(|| {
        app.document_path
            .as_deref()
            .and_then(|p| Path::new(p).parent())
            .map(|p| p.to_string_lossy().into_owned())
    });
    let Some(path) = start else {
        app.error = Some("no workspace to browse for mentions".to_string());
        return;
    };
    bridge.send(Command::ListDirectory { path });
    app.overlay = Overlay::Mentions;
    app.picker_index = 0;
}

/// Mention-picker keys: pick a file to attach (`Task.mentions`), or close.
fn handle_mentions_key(key: KeyEvent, app: &mut AppState, bridge: &Bridge) {
    let count = app.directory.as_ref().map(|d| d.entries.len()).unwrap_or(0);
    match key.code {
        KeyCode::Esc => app.overlay = Overlay::None,
        KeyCode::Up => app.picker_index = app.picker_index.saturating_sub(1),
        KeyCode::Down => {
            if app.picker_index + 1 < count {
                app.picker_index += 1;
            }
        }
        KeyCode::Enter => {
            if let Some(entry) = app
                .directory
                .as_ref()
                .and_then(|d| d.entries.get(app.picker_index))
            {
                app.mentions.push(entry.path.clone());
                app.input.push_str(&format!("@{} ", entry.name));
                app.overlay = Overlay::None;
            }
        }
        _ => {}
    }
    let _ = bridge;
}

/// Locate ambiguity-picker keys (ADR-0048 §4): choose a candidate or cancel.
fn handle_locate_key(key: KeyEvent, app: &mut AppState, bridge: &Bridge) {
    let count = app
        .last_locate
        .as_ref()
        .and_then(|l| l.candidates.as_ref())
        .map(|c| c.len())
        .unwrap_or(0);
    match key.code {
        KeyCode::Esc => {
            if let Some(turn_id) = app.turn_id.clone() {
                bridge.send(Command::ResolveLocate {
                    turn_id,
                    chunk_key: None,
                    cancel: true,
                });
            }
            app.overlay = Overlay::None;
        }
        KeyCode::Up => app.picker_index = app.picker_index.saturating_sub(1),
        KeyCode::Down => {
            if app.picker_index + 1 < count {
                app.picker_index += 1;
            }
        }
        KeyCode::Enter => {
            if let (Some(turn_id), Some(chunk_key)) = (
                app.turn_id.clone(),
                app.last_locate
                    .as_ref()
                    .and_then(|l| l.candidates.as_ref())
                    .and_then(|c| c.get(app.picker_index))
                    .map(|c| c.chunk_key.clone()),
            ) {
                bridge.send(Command::ResolveLocate {
                    turn_id,
                    chunk_key: Some(chunk_key),
                    cancel: false,
                });
            }
            app.overlay = Overlay::None;
        }
        _ => {}
    }
}

fn cycle_mode(app: &mut AppState, delta: isize) {
    let count = app.modes.len();
    if count == 0 {
        return;
    }
    let count = count as isize;
    let current = app.active_mode_index() as isize;
    let next = ((current + delta) % count + count) % count;
    app.active_mode = Some(app.modes[next as usize].name.clone());
}

/// Render one frame; returns the maximum chat scroll offset for the frame.
fn render(frame: &mut Frame, app: &AppState, desired_scroll: u16) -> u16 {
    let area = frame.area();
    let show_diff = app.candidate.is_some() || !app.diffs.is_empty();

    let mut constraints = vec![Constraint::Length(3), Constraint::Min(5)];
    if show_diff {
        constraints.push(Constraint::Length(8));
    }
    constraints.push(Constraint::Length(3));
    constraints.push(Constraint::Length(2));
    let chunks = Layout::default()
        .direction(Direction::Vertical)
        .constraints(constraints)
        .split(area);

    render_tabs(frame, chunks[0], app);
    let max_scroll = render_main(frame, chunks[1], app, desired_scroll);
    let input_index = chunks.len() - 2;
    let status_index = chunks.len() - 1;
    if show_diff {
        render_diff(frame, chunks[2], app);
    }
    render_input(frame, chunks[input_index], app);
    render_status(frame, chunks[status_index], app);

    match app.overlay {
        Overlay::Directory => render_directory_overlay(frame, area, app),
        Overlay::Sessions => render_sessions_overlay(frame, area, app),
        Overlay::Tray => render_tray_overlay(frame, area, app),
        Overlay::Mentions => render_mentions_overlay(frame, area, app),
        Overlay::Locate => render_locate_overlay(frame, area, app),
        Overlay::None => {}
    }
    if app.rename.is_some() {
        render_rename_overlay(frame, area, app);
    }
    if app.query_edit.is_some() {
        render_query_overlay(frame, area, app);
    }
    max_scroll
}

/// The main area: chat, optionally beside the reader and/or inspector panes.
fn render_main(frame: &mut Frame, area: Rect, app: &AppState, desired_scroll: u16) -> u16 {
    if !app.reader_visible && !app.inspector_visible {
        return render_chat(frame, area, app, desired_scroll);
    }
    let cols = Layout::default()
        .direction(Direction::Horizontal)
        .constraints([Constraint::Percentage(58), Constraint::Percentage(42)])
        .split(area);
    let max_scroll = render_chat(frame, cols[0], app, desired_scroll);
    match (app.reader_visible, app.inspector_visible) {
        (true, true) => {
            let rows = Layout::default()
                .direction(Direction::Vertical)
                .constraints([Constraint::Percentage(58), Constraint::Percentage(42)])
                .split(cols[1]);
            render_reader(frame, rows[0], app);
            render_inspector(frame, rows[1], app);
        }
        (true, false) => render_reader(frame, cols[1], app),
        (false, true) => render_inspector(frame, cols[1], app),
        (false, false) => {}
    }
    max_scroll
}

/// The read-only reader pane (ADR-0050): the engine's block markdown, joined
/// with `\n\n` and rendered through `tui-markdown`. No edit affordance.
fn render_reader(frame: &mut Frame, area: Rect, app: &AppState) {
    let markdown = app
        .blocks
        .iter()
        .map(|b| b.text.as_str())
        .collect::<Vec<_>>()
        .join("\n\n");
    let paragraph = if markdown.trim().is_empty() {
        Paragraph::new("(no document open)")
    } else {
        Paragraph::new(tui_markdown::from_str(&markdown))
    };
    let title = format!(
        "Reader · {} (read-only)",
        app.document_path.as_deref().unwrap_or("-")
    );
    frame.render_widget(
        paragraph
            .block(Block::default().borders(Borders::ALL).title(title))
            .wrap(Wrap { trim: false }),
        area,
    );
}

/// The inspector pane (ADR-0044): the engine's meter + context snapshot, plus
/// the tray summary. Engine data only; the client computes nothing.
fn render_inspector(frame: &mut Frame, area: Rect, app: &AppState) {
    let mut lines: Vec<Line> = Vec::new();
    if let Some(meter) = &app.meter {
        lines.push(Line::from(format!(
            "meter · sys {} tool {} rag {} hist {} ment {} user {} think {}{} comp {}",
            meter.system,
            meter.tools,
            meter.rag,
            meter.history,
            meter.mentions,
            meter.user,
            meter.thinking,
            if meter.thinking_approx.unwrap_or(false) {
                "~"
            } else {
                ""
            },
            meter.completion,
        )));
        if let Some(m) = &meter.measurement {
            lines.push(Line::from(format!(
                "{} · {}ms · prompt {} think {} comp {} · win {:.0}%",
                m.model.as_deref().unwrap_or("-"),
                m.latency_ms.unwrap_or(0),
                m.prompt_tokens.unwrap_or(0),
                m.thinking_tokens.unwrap_or(0),
                m.completion_tokens.unwrap_or(0),
                m.window_utilization.unwrap_or(0.0) * 100.0,
            )));
        }
    }
    if let Some(snap) = &app.last_context {
        lines.push(Line::from(format!(
            "context · {} msgs · {} chunks · {} drops{}",
            snap.messages.len(),
            snap.chunks.len(),
            snap.drops.len(),
            if snap.cancelled.unwrap_or(false) {
                " · cancelled"
            } else {
                ""
            },
        )));
        if let Some(t) = &snap.thinking {
            lines.push(Line::from(format!(
                "thinking · {} eff={}",
                t.level.as_str(),
                t.effective
            )));
            if t.escalated.unwrap_or(false) {
                lines.push(Line::from(format!(
                    "  escalated: {}",
                    t.escalation_reason.as_deref().unwrap_or("?")
                )));
            }
            if t.unsupported.unwrap_or(false) {
                lines.push(Line::from("  unsupported (thinking on)"));
            }
            if t.truncated.unwrap_or(false) {
                lines.push(Line::from("  thinking-truncated"));
            }
        }
        if let Some(w) = &snap.window {
            lines.push(Line::from(format!(
                "window · {} / {} reserve {}",
                w.used.unwrap_or(0),
                w.context_length.unwrap_or(0),
                w.reserve.unwrap_or(0),
            )));
        }
        if let Some(sb) = &snap.session_budget {
            lines.push(Line::from(format!(
                "budget · {} / {}{}{}",
                sb.used.unwrap_or(0),
                sb.budget.unwrap_or(0),
                if sb.soft.unwrap_or(false) {
                    " soft"
                } else {
                    ""
                },
                if sb.hard.unwrap_or(false) {
                    " hard"
                } else {
                    ""
                },
            )));
        }
        if snap.compacted.is_some() {
            lines.push(Line::from("compacted (metered summary)"));
        }
        for d in &snap.drops {
            lines.push(Line::from(format!(
                "drop {} × {} ({}){}",
                d.component.as_str(),
                d.count,
                d.reason,
                if d.human_override.unwrap_or(false) {
                    " [override]"
                } else {
                    ""
                },
            )));
        }
        for c in &snap.chunks {
            lines.push(Line::from(format!(
                "chunk {}{}{}",
                c.path.as_deref().unwrap_or(&c.block_id),
                c.chunk_key
                    .as_deref()
                    .map(|k| format!(" #{k}"))
                    .unwrap_or_default(),
                if c.pinned.unwrap_or(false) {
                    " [pinned]"
                } else {
                    ""
                },
            )));
        }
    }
    if let Some(l) = &app.last_locate {
        lines.push(Line::from(format!(
            "locate · {}{}",
            l.status.as_str(),
            l.path
                .as_deref()
                .map(|p| format!(" {p}"))
                .unwrap_or_default(),
        )));
    }
    if !app.mentions.is_empty() {
        lines.push(Line::from(format!("mentions · {}", app.mentions.len())));
    }
    if let Some(p) = &app.session_policy {
        lines.push(Line::from(format!(
            "tray · autoRag {} · pins {} · excludes {} · query {}",
            p.auto_rag.unwrap_or(true),
            p.pinned.as_ref().map_or(0, |v| v.len()),
            p.excluded.as_ref().map_or(0, |v| v.len()),
            p.retrieval_query.as_deref().unwrap_or("(user input)"),
        )));
    }
    if app.override_next_turn {
        lines.push(Line::from(Span::styled(
            "override next turn: on",
            Style::default().fg(Color::Yellow),
        )));
    }
    frame.render_widget(
        Paragraph::new(lines)
            .block(
                Block::default()
                    .borders(Borders::ALL)
                    .title("Inspector · Ctrl+T tray"),
            )
            .wrap(Wrap { trim: false }),
        area,
    );
}

fn render_tabs(frame: &mut Frame, area: Rect, app: &AppState) {
    let titles: Vec<Line> = app
        .modes
        .iter()
        .map(|mode| Line::from(format!(" {} ", mode.name)))
        .collect();
    let titles = if titles.is_empty() {
        vec![Line::from(" (no presets) ")]
    } else {
        titles
    };
    let title = match &app.workspace {
        Some(ws) => format!("Presets · {} ({})", ws.name, ws.root),
        None => "Presets".to_string(),
    };
    let tabs = Tabs::new(titles)
        .select(app.active_mode_index())
        .block(Block::default().borders(Borders::ALL).title(title))
        .highlight_style(
            Style::default()
                .fg(Color::Black)
                .bg(Color::Cyan)
                .add_modifier(Modifier::BOLD),
        );
    frame.render_widget(tabs, area);
}

fn render_chat(frame: &mut Frame, area: Rect, app: &AppState, desired_scroll: u16) -> u16 {
    let mut lines: Vec<Line> = Vec::new();
    for message in &app.messages {
        let prefix = match message.role {
            Role::User => "you › ",
            Role::Assistant => "ai  › ",
            Role::System => "· ",
        };
        push_prefixed(&mut lines, prefix, &message.text);
    }
    if let Some(stream) = &app.streaming {
        if !stream.is_empty() {
            push_prefixed(&mut lines, "ai  › ", stream);
        }
    }
    if !app.thinking.is_empty() {
        lines.push(Line::from(Span::styled(
            "thinking…",
            Style::default().fg(Color::Magenta),
        )));
    }
    if lines.is_empty() {
        lines.push(Line::from(
            "Type a message and press Enter. Ctrl+C quits, Tab switches preset.",
        ));
    }
    if let Some(err) = &app.error {
        lines.push(Line::from(Span::styled(
            format!("error: {err}"),
            Style::default().fg(Color::Red),
        )));
    }

    let inner_width = area.width.saturating_sub(2).max(1);
    let total = wrapped_height(&lines, inner_width);
    let view = area.height.saturating_sub(2);
    let max_scroll = total.saturating_sub(view);
    let scroll = desired_scroll.min(max_scroll);

    let paragraph = Paragraph::new(lines)
        .block(Block::default().borders(Borders::ALL).title("Chat"))
        .wrap(Wrap { trim: false })
        .scroll((scroll, 0));
    frame.render_widget(paragraph, area);
    max_scroll
}

fn render_diff(frame: &mut Frame, area: Rect, app: &AppState) {
    let mut lines: Vec<Line> = Vec::new();
    if let Some(candidate) = &app.candidate {
        lines.push(Line::from(vec![
            Span::styled(
                "candidate block ",
                Style::default().add_modifier(Modifier::BOLD),
            ),
            Span::raw(candidate.block_id.clone()),
        ]));
        if let Some(err) = &candidate.error {
            lines.push(Line::from(Span::styled(
                format!("candidate error: {err}"),
                Style::default().fg(Color::Red),
            )));
        } else {
            lines.push(Line::from(
                "Ctrl+A approve (write-through) · Ctrl+O overwrite on conflict",
            ));
        }
    }
    for diff in &app.diffs {
        if let Some(block_id) = &diff.block_id {
            lines.push(Line::from(format!("diff · {block_id}")));
        }
        if let Some(deletions) = &diff.deletions {
            lines.push(Line::from(Span::styled(
                format!("- {}", deletions.join(" ")),
                Style::default().fg(Color::Red),
            )));
        }
        if let Some(insertions) = &diff.insertions {
            lines.push(Line::from(Span::styled(
                format!("+ {}", insertions.join(" ")),
                Style::default().fg(Color::Green),
            )));
        }
        if let Some(edits) = &diff.edits {
            for edit in edits {
                lines.push(Line::from(format!("edit · {}", edit.block_id)));
            }
        }
    }
    let paragraph = Paragraph::new(lines)
        .block(Block::default().borders(Borders::ALL).title("Diff preview"))
        .wrap(Wrap { trim: false });
    frame.render_widget(paragraph, area);
}

fn render_input(frame: &mut Frame, area: Rect, app: &AppState) {
    let paragraph = Paragraph::new(app.input.as_str()).block(
        Block::default()
            .borders(Borders::ALL)
            .title("Message (Enter send · Ctrl+X cancel · Ctrl+W files · Ctrl+S sessions)"),
    );
    frame.render_widget(paragraph, area);
    let cursor_x = area.x + 1 + app.input.chars().count().min(u16::MAX as usize) as u16;
    if cursor_x < area.x + area.width.saturating_sub(1) {
        frame.set_cursor_position(Position::new(cursor_x, area.y + 1));
    }
}

fn render_status(frame: &mut Frame, area: Rect, app: &AppState) {
    let mut spans: Vec<Span> = Vec::new();
    match &app.connection {
        ConnectionState::Connecting => {
            spans.push(Span::styled(
                " connecting… ",
                Style::default().fg(Color::Yellow),
            ));
        }
        ConnectionState::Connected { base_url } => {
            spans.push(Span::styled(
                format!(" {base_url} "),
                Style::default().fg(Color::Green),
            ));
        }
        ConnectionState::Unreachable { error } => {
            spans.push(Span::styled(
                format!(" unreachable: {error} "),
                Style::default().fg(Color::Red),
            ));
        }
    }
    if let Some(session_id) = &app.session_id {
        let title = app.session_title.as_deref().unwrap_or("untitled");
        spans.push(Span::raw(format!("│ session {title} ")));
        let _ = session_id;
    }
    if let Some(model) = &app.status.used_model {
        let degraded = if app.status.degraded {
            " (degraded)"
        } else {
            ""
        };
        spans.push(Span::raw(format!("│ model {model}{degraded} ")));
    }
    if app.turn_active {
        spans.push(Span::styled(
            "│ generating… ",
            Style::default().fg(Color::Cyan),
        ));
    }
    if app.cancelled {
        spans.push(Span::styled(
            "│ cancelled ",
            Style::default().fg(Color::Yellow),
        ));
    }
    if let Some(path) = &app.status.target_path {
        spans.push(Span::raw(format!("│ {path} ")));
    }
    if let Some(conflict) = &app.status.conflict {
        spans.push(Span::styled(
            format!("│ {conflict} "),
            Style::default().fg(Color::Red).add_modifier(Modifier::BOLD),
        ));
    } else if app.status.written_through {
        spans.push(Span::styled(
            "│ written-through ",
            Style::default().fg(Color::Green),
        ));
    }
    frame.render_widget(Paragraph::new(Line::from(spans)), area);
}

fn render_directory_overlay(frame: &mut Frame, area: Rect, app: &AppState) {
    let popup = centered_rect(70, 70, area);
    frame.render_widget(Clear, popup);
    let mut lines: Vec<Line> = Vec::new();
    let path = app
        .directory
        .as_ref()
        .map(|d| d.path.clone())
        .unwrap_or_default();
    let cursor = |selected: bool| if selected { "› " } else { "  " };
    lines.push(Line::from(format!("{}..", cursor(app.picker_index == 0))));
    if let Some(listing) = &app.directory {
        for (i, entry) in listing.entries.iter().enumerate() {
            let suffix = if entry.is_dir { "/" } else { "" };
            lines.push(Line::from(format!(
                "{}{}{}",
                cursor(app.picker_index == i + 1),
                entry.name,
                suffix
            )));
        }
    }
    let block = Block::default()
        .borders(Borders::ALL)
        .title(format!("Open file · {path}"));
    let visible = popup.height.saturating_sub(2) as usize;
    let offset = app.picker_index.saturating_sub(visible.saturating_sub(1));
    let paragraph = Paragraph::new(lines)
        .block(block)
        .wrap(Wrap { trim: true })
        .scroll((offset as u16, 0));
    frame.render_widget(paragraph, popup);
}

fn render_sessions_overlay(frame: &mut Frame, area: Rect, app: &AppState) {
    let popup = centered_rect(60, 60, area);
    frame.render_widget(Clear, popup);
    let mut lines: Vec<Line> = Vec::new();
    for (i, session) in app.sessions.iter().enumerate() {
        let title = session.title.as_deref().unwrap_or("untitled");
        let doc = session.document_id.as_str();
        let cursor = if app.picker_index == i { "› " } else { "  " };
        lines.push(Line::from(format!("{cursor}{title}  ·  {doc}")));
    }
    if lines.is_empty() {
        lines.push(Line::from("(no sessions)"));
    }
    let block = Block::default()
        .borders(Borders::ALL)
        .title("Sessions · Enter resume · n new · r rename · Esc close");
    let visible = popup.height.saturating_sub(2) as usize;
    let offset = app.picker_index.saturating_sub(visible.saturating_sub(1));
    let paragraph = Paragraph::new(lines)
        .block(block)
        .wrap(Wrap { trim: true })
        .scroll((offset as u16, 0));
    frame.render_widget(paragraph, popup);
}

fn render_rename_overlay(frame: &mut Frame, area: Rect, app: &AppState) {
    let popup = centered_rect(50, 15, area);
    frame.render_widget(Clear, popup);
    let text = app.rename.clone().unwrap_or_default();
    let paragraph = Paragraph::new(text).block(
        Block::default()
            .borders(Borders::ALL)
            .title("Rename · Enter save · Esc cancel"),
    );
    frame.render_widget(paragraph, popup);
}

fn render_query_overlay(frame: &mut Frame, area: Rect, app: &AppState) {
    let popup = centered_rect(60, 15, area);
    frame.render_widget(Clear, popup);
    let text = app.query_edit.clone().unwrap_or_default();
    let paragraph = Paragraph::new(text).block(
        Block::default()
            .borders(Borders::ALL)
            .title("Retrieval query · Enter save · Esc cancel"),
    );
    frame.render_widget(paragraph, popup);
}

fn render_tray_overlay(frame: &mut Frame, area: Rect, app: &AppState) {
    let popup = centered_rect(70, 60, area);
    frame.render_widget(Clear, popup);
    let mut lines: Vec<Line> = Vec::new();
    if let Some(snap) = &app.last_context {
        for (i, chunk) in snap.chunks.iter().enumerate() {
            let cursor = if i == app.tray_index { "› " } else { "  " };
            let label = chunk.path.as_deref().unwrap_or(&chunk.block_id);
            let mut marks = String::new();
            if chunk.pinned.unwrap_or(false) {
                marks.push_str(" [pinned]");
            }
            if chunk.human_override.unwrap_or(false) {
                marks.push_str(" [override]");
            }
            lines.push(Line::from(format!("{cursor}{label}{marks}")));
        }
    }
    if lines.is_empty() {
        lines.push(Line::from("(no retrieved chunks yet — run a turn)"));
    }
    let auto = app
        .session_policy
        .as_ref()
        .and_then(|p| p.auto_rag)
        .unwrap_or(true);
    lines.push(Line::from(format!(
        "auto-RAG: {auto}  ·  override next turn: {}",
        app.override_next_turn
    )));
    lines.push(Line::from(
        "p pin · x exclude · u remove · a toggle auto-RAG · q query · o override · Esc close",
    ));
    let paragraph = Paragraph::new(lines)
        .block(Block::default().borders(Borders::ALL).title("Context tray"))
        .wrap(Wrap { trim: true });
    frame.render_widget(paragraph, popup);
}

fn render_mentions_overlay(frame: &mut Frame, area: Rect, app: &AppState) {
    let popup = centered_rect(70, 60, area);
    frame.render_widget(Clear, popup);
    let mut lines: Vec<Line> = Vec::new();
    if let Some(listing) = &app.directory {
        for (i, entry) in listing.entries.iter().enumerate() {
            if entry.is_dir {
                continue;
            }
            let cursor = if app.picker_index == i { "› " } else { "  " };
            lines.push(Line::from(format!("{cursor}{}", entry.name)));
        }
    }
    if lines.is_empty() {
        lines.push(Line::from(
            "(no files here — navigate or browse a directory)",
        ));
    }
    lines.push(Line::from("Enter attach · Esc close"));
    frame.render_widget(
        Paragraph::new(lines)
            .block(
                Block::default()
                    .borders(Borders::ALL)
                    .title("Mention a file (Task.mentions)"),
            )
            .wrap(Wrap { trim: true }),
        popup,
    );
}

fn render_locate_overlay(frame: &mut Frame, area: Rect, app: &AppState) {
    let popup = centered_rect(70, 60, area);
    frame.render_widget(Clear, popup);
    let mut lines: Vec<Line> = Vec::new();
    if let Some(locate) = &app.last_locate {
        if let Some(candidates) = &locate.candidates {
            for (i, c) in candidates.iter().enumerate() {
                let cursor = if app.picker_index == i { "› " } else { "  " };
                lines.push(Line::from(format!("{cursor}{}  ({:.2})", c.path, c.score)));
                lines.push(Line::from(format!("    {}", c.text_preview)));
            }
        }
    }
    if lines.is_empty() {
        lines.push(Line::from("(no candidates)"));
    }
    lines.push(Line::from("Enter anchor · Esc cancel to plain chat"));
    frame.render_widget(
        Paragraph::new(lines)
            .block(
                Block::default()
                    .borders(Borders::ALL)
                    .title("Locate · choose the anchor"),
            )
            .wrap(Wrap { trim: true }),
        popup,
    );
}

/// The tray's selected snapshot chunk, if any.
fn selected_chunk(app: &AppState) -> Option<crate::gen::ContextChunk> {
    app.last_context
        .as_ref()
        .and_then(|s| s.chunks.get(app.tray_index))
        .cloned()
}

fn base_policy(app: &AppState) -> crate::gen::ContextPolicy {
    app.session_policy.clone().unwrap_or_default()
}

fn session_policy_with_query(app: &AppState, query: Option<String>) -> crate::gen::ContextPolicy {
    let mut policy = base_policy(app);
    policy.retrieval_query = query;
    policy
}

fn session_policy_with_auto_rag(app: &AppState) -> crate::gen::ContextPolicy {
    let mut policy = base_policy(app);
    let current = policy.auto_rag.unwrap_or(true);
    policy.auto_rag = Some(!current);
    policy
}

fn chunk_ref(chunk: &crate::gen::ContextChunk) -> crate::gen::ChunkRef {
    crate::gen::ChunkRef {
        chunk_key: chunk.chunk_key.clone(),
        hash: None,
        path: chunk.path.clone().unwrap_or_default(),
    }
}

fn same_ref(a: &crate::gen::ChunkRef, b: &crate::gen::ChunkRef) -> bool {
    a.chunk_key == b.chunk_key && a.path == b.path
}

/// Tray keys: pin/exclude/remove the selected chunk, toggle auto-RAG, edit the
/// retrieval query, or hold the tray as the next turn's override. Every edit
/// persists the session policy (ADR-0049 §8) — decisions, never payload text.
fn handle_tray_key(key: KeyEvent, app: &mut AppState, bridge: &Bridge) {
    let chunk_count = app
        .last_context
        .as_ref()
        .map(|c| c.chunks.len())
        .unwrap_or(0);
    match key.code {
        KeyCode::Esc => app.overlay = Overlay::None,
        KeyCode::Up => app.tray_index = app.tray_index.saturating_sub(1),
        KeyCode::Down => {
            if app.tray_index + 1 < chunk_count {
                app.tray_index += 1;
            }
        }
        KeyCode::Char('p') => {
            if let Some(chunk) = selected_chunk(app) {
                let mut policy = base_policy(app);
                let mut pinned = policy.pinned.clone().unwrap_or_default();
                let reference = chunk_ref(&chunk);
                if !pinned.iter().any(|r| same_ref(r, &reference)) {
                    pinned.push(reference);
                }
                policy.pinned = Some(pinned);
                bridge.send(Command::PutSessionContext { policy });
            }
        }
        KeyCode::Char('x') => {
            if let Some(chunk) = selected_chunk(app) {
                let mut policy = base_policy(app);
                let mut excluded = policy.excluded.clone().unwrap_or_default();
                let reference = chunk_ref(&chunk);
                if !excluded.iter().any(|r| same_ref(r, &reference)) {
                    excluded.push(reference);
                }
                policy.excluded = Some(excluded);
                bridge.send(Command::PutSessionContext { policy });
            }
        }
        KeyCode::Char('u') => {
            if let Some(chunk) = selected_chunk(app) {
                let reference = chunk_ref(&chunk);
                let mut policy = base_policy(app);
                if let Some(pinned) = policy.pinned.as_mut() {
                    pinned.retain(|r| !same_ref(r, &reference));
                }
                if let Some(excluded) = policy.excluded.as_mut() {
                    excluded.retain(|r| !same_ref(r, &reference));
                }
                bridge.send(Command::PutSessionContext { policy });
            }
        }
        KeyCode::Char('a') => {
            let policy = session_policy_with_auto_rag(app);
            bridge.send(Command::PutSessionContext { policy });
        }
        KeyCode::Char('q') => {
            let query = app
                .session_policy
                .as_ref()
                .and_then(|p| p.retrieval_query.clone())
                .unwrap_or_default();
            app.query_edit = Some(query);
        }
        KeyCode::Char('o') => app.override_next_turn = !app.override_next_turn,
        _ => {}
    }
}

fn centered_rect(percent_x: u16, percent_y: u16, area: Rect) -> Rect {
    let vertical = Layout::default()
        .direction(Direction::Vertical)
        .constraints([
            Constraint::Percentage((100 - percent_y) / 2),
            Constraint::Percentage(percent_y),
            Constraint::Percentage((100 - percent_y) / 2),
        ])
        .split(area);
    Layout::default()
        .direction(Direction::Horizontal)
        .constraints([
            Constraint::Percentage((100 - percent_x) / 2),
            Constraint::Percentage(percent_x),
            Constraint::Percentage((100 - percent_x) / 2),
        ])
        .split(vertical[1])[1]
}

fn push_prefixed(lines: &mut Vec<Line<'static>>, prefix: &'static str, text: &str) {
    let mut first = true;
    for raw in text.split('\n') {
        let head = if first { prefix } else { "     " };
        lines.push(Line::from(format!("{head}{raw}")));
        first = false;
    }
}

fn wrapped_height(lines: &[Line], width: u16) -> u16 {
    if width == 0 {
        return 0;
    }
    let width = width as usize;
    let mut total = 0usize;
    for line in lines {
        let cells: usize = line.spans.iter().map(|s| s.content.chars().count()).sum();
        total += if cells == 0 { 1 } else { cells.div_ceil(width) };
    }
    total.min(u16::MAX as usize) as u16
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::state::AppState;

    fn app_with_policy(auto_rag: Option<bool>, query: Option<&str>) -> AppState {
        let mut app = AppState::default();
        app.session_policy = Some(crate::gen::ContextPolicy {
            auto_rag,
            retrieval_query: query.map(str::to_string),
            ..Default::default()
        });
        app
    }

    #[test]
    fn auto_rag_toggle_flips_the_persisted_flag() {
        // Default (absent) reads as on, so the toggle turns it off.
        let app = AppState::default();
        let policy = session_policy_with_auto_rag(&app);
        assert_eq!(policy.auto_rag, Some(false));

        let app = app_with_policy(Some(false), None);
        let policy = session_policy_with_auto_rag(&app);
        assert_eq!(policy.auto_rag, Some(true));
    }

    #[test]
    fn query_edit_replaces_only_the_query() {
        let app = app_with_policy(Some(true), Some("old"));
        let policy = session_policy_with_query(&app, Some("new".to_string()));
        assert_eq!(policy.retrieval_query.as_deref(), Some("new"));
        assert_eq!(policy.auto_rag, Some(true));
    }

    #[test]
    fn chunk_ref_identity_matches_on_key_and_path() {
        let a = crate::gen::ChunkRef {
            chunk_key: Some("/v/a.md#0".to_string()),
            hash: None,
            path: "/v/a.md".to_string(),
        };
        let b = crate::gen::ChunkRef {
            chunk_key: Some("/v/a.md#0".to_string()),
            hash: Some("ignored".to_string()),
            path: "/v/a.md".to_string(),
        };
        let c = crate::gen::ChunkRef {
            chunk_key: Some("/v/a.md#1".to_string()),
            hash: None,
            path: "/v/a.md".to_string(),
        };
        assert!(same_ref(&a, &b));
        assert!(!same_ref(&a, &c));
    }
}
