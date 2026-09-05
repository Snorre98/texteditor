# language: en
Feature: Sessions
  Multiple persisted chat sessions per document, runnable concurrently, with
  selection-anchored bubbles in the full text editor.
  Normative per ADR-0026.

  Scenario: A session is created per selection anchor
    Given a document with block P
    When the user highlights block P and opens a chat bubble
    Then a session is created with AnchorBlockID = P

  Scenario: Re-highlighting the same block resumes, not recreates
    Given a session already anchors block P
    When the user re-highlights block P
    Then the same session is resumed, with its prior message history intact

  Scenario: A document-level chat is an unanchored session
    Given a document
    When the user opens the free-floating chat
    Then a session is created with AnchorBlockID nil

  Scenario: Multiple sessions per file
    Given a document
    When the user creates a free-floating session and two block-anchored sessions
    Then ListByDocument returns all three sessions

  Scenario: Sessions run turns concurrently
    Given two sessions, each streaming a turn
    When both turns run at once
    Then each session receives its own token/candidate/diff/done events
    And events are correlated by turnID, un-mangled across sessions

  Scenario: A session persists across a client disconnect
    Given a session with message history
    When the client disconnects and later reconnects
    Then Resume(id) returns the session with its full history

  Scenario: Per-session token budget is enforced
    Given a session with TokenBudget set
    When the next turn would cross the cumulative token cap
    Then the meter surfaces a session-budget-exceeded error
    And the budget violation is visible in the meter, not silent

  Scenario: Concurrent edits to one document are queue-serialized
    Given two sessions anchored in the same document each propose an edit
    When both ApplyEdit calls arrive
    Then the Document store applies them in arrival order, never interleaved mid-block
    And neither edit is rejected

  Scenario: A second session's edit waits, never clobbers
    Given session A's edit is in flight on document D
    When session B issues an edit on document D
    Then B's edit waits for A's to commit and is applied afterward
