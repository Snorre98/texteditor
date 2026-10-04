# language: en
Feature: Dumb clients
  Clients contain no domain logic; everything is generated from the OpenAPI
  contract and routed to the engine.
  Normative per ADR-0002, ADR-0013, ADR-0017, ADR-0046, ADR-0050.

  Scenario: A client is generated, not hand-coded
    Given the OpenAPI spec is updated with a new endpoint
    When the TS client is regenerated (Hey API + Zod) and the Rust client (openapi-to-rust)
    Then both clients gain a typed client with no hand-written types

  Scenario: Client requests route through the engine
    Given the TUI issues an edit command
    When the engine processes it
    Then the edit is versioned by the engine, not by the client
    And a second client (Tauri) sees the same version history

  Scenario: The token meter is engine-sourced
    Given a live stream
    When the client renders the meter
    Then the numbers come from meter SSE events, never client-side estimates

  Scenario: TS responses are runtime-validated with Zod
    Given the web/LAN target serves the client off trusted localhost
    When a response arrives
    Then the TS client validates the body against a generated Zod schema

  Scenario: The TUI renders via Ratatui
    Given the Ratatui TUI is built
    When a panel (chat, reader, meter, diff) updates
    Then it renders from engine-sourced state snapshots through Ratatui widgets
    And the generated Rust client and the hand-written SSE decoder are its only transport

  Scenario: The document reader renders engine-sourced blocks
    Given a document is open
    When the reader pane renders
    Then it renders the canonical markdown joined from GET /documents/{id}/blocks
    And the client holds no document state of record and never writes
