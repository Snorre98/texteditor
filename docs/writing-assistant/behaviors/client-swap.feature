# language: en
Feature: Dumb clients
  Clients contain no domain logic; everything is generated from the OpenAPI
  contract and routed to the engine.
  Normative per ADR-0002, ADR-0013, ADR-0017, ADR-0023.

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

  Scenario: The TUI renders via Solid
    Given the OpenTUI TUI is built
    When a panel (chat, meter, diff) updates
    Then it renders reactively via Solid components, not raw TS imperative updates
