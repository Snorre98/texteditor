# language: en
Feature: Chat window
  The Tauri editor's assistant surface is a single floating, draggable window
  over the workspace, with a session list, full chat features, and client-local
  persistence. Normative per ADR-0041.

  Scenario: The chat floats over the workspace
    Given the editor is open
    Then the chat window overlays the document without changing its layout
    And the editor owns the full viewport behind the window

  Scenario: The window drags anywhere
    Given the chat window is floating
    When the user drags its header to a new position
    Then the window follows the pointer, clamped to the viewport
    And the new position is persisted for the next launch

  Scenario: The window resizes with clamps
    Given the chat window is floating
    When the user drags a resize handle beyond the viewport edge
    Then the size clamps to the viewport bounds
    And the new size is persisted for the next launch

  Scenario: The window minimizes to an orb and restores
    Given the chat window is open
    When the user minimizes it
    Then a floating orb remains over the workspace
    And clicking the orb restores the window at its last position and size

  Scenario: The window snaps to an edge and docks
    Given the user drags the window toward the right edge
    When it enters the snap zone and is released
    Then it docks to the right edge and the editor reflows beside it
    And the docked mode is persisted for the next launch

  Scenario: The session list shows every session of the document
    Given a document with past sessions
    When the user opens the session list
    Then all anchored and free sessions are listed
    And selecting one restores its full message history

  Scenario: A new chat creates a session
    Given a document
    When the user starts a new chat
    Then a session is created and becomes the active session

  Scenario: Selection-anchored sessions join the list
    Given the user has text selected
    When they ask about the selection
    Then an anchored session appears in the session list and becomes active
    And re-asking about the same block resumes the same session

  Scenario: Concurrent sessions show their working state
    Given two sessions streaming turns concurrently
    Then each session's entry shows a working indicator
    And each turn's tokens and steps stay separate

  Scenario: Assistant messages render sanitized markdown
    Given an assistant message containing markdown and a script tag
    Then the message renders as formatted markdown
    And no script executes

  Scenario: The retry action resends the last user input
    Given a completed turn
    When the user retries the last message
    Then a new turn runs in the same session with the same user input

  Scenario: Meter and retrieval panels collapse
    Given a turn with meter and retrieval data
    When the user collapses the panels
    Then only their headers remain visible

  Scenario: Window state survives a relaunch
    Given a user configured dock, position, size, and minimized state
    When the app relaunches with the same document
    Then the window restores the persisted state
