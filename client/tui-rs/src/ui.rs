//! The Ratatui widgets and the synchronous crossterm event/render loop
//! (ADR-0046 §1, §4, §6).
//!
//! The loop renders from the render-only [`AppState`] snapshot and never computes
//! domain values (ADR-0013 §3). Bracketed paste is enabled so multi-line pastes
//! insert verbatim — required by `/locate` in E2.

use std::io::{self, Stdout};
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
use ratatui::widgets::{Block, Borders, Paragraph, Tabs, Wrap};
use ratatui::{Frame, Terminal};

use crate::bridge::{Bridge, Command};
use crate::state::{AppState, ConnectionState, Role};

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
        KeyCode::Enter => {
            if !app.turn_active {
                if let Some(mode) = app.active_mode.clone() {
                    let input = app.input.trim().to_string();
                    if !input.is_empty() {
                        app.begin_turn(input.clone());
                        bridge.send(Command::SendTurn { mode, input });
                        *follow = true;
                    }
                }
            }
        }
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
    constraints.push(Constraint::Length(1));
    let chunks = Layout::default()
        .direction(Direction::Vertical)
        .constraints(constraints)
        .split(area);

    render_tabs(frame, chunks[0], app);
    let max_scroll = render_chat(frame, chunks[1], app, desired_scroll);
    let input_index = chunks.len() - 2;
    let status_index = chunks.len() - 1;
    if show_diff {
        render_diff(frame, chunks[2], app);
    }
    render_input(frame, chunks[input_index], app);
    render_status(frame, chunks[status_index], app);
    max_scroll
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
    let tabs = Tabs::new(titles)
        .select(app.active_mode_index())
        .block(Block::default().borders(Borders::ALL).title("Presets"))
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
            .title("Message (Enter send · Ctrl+C quit)"),
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
    if let Some(model) = &app.status.used_model {
        let degraded = if app.status.degraded {
            " (degraded)"
        } else {
            ""
        };
        spans.push(Span::raw(format!("│ model {model}{degraded} ")));
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
