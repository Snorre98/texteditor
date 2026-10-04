# language: en
Feature: Context inspection — every turn's context is explainable and replayable
  Every turn persists a snapshot of the assembled payload: each message's
  component and provenance, the retrieval and decision outcomes, the budget
  accounting, and every truncation or drop as a labeled record. Clients render
  the snapshot; they never reconstruct it.
  Normative per ADR-0044, ADR-0011, ADR-0024, ADR-0036.

  Scenario: A completed turn persists a context snapshot
    Given a turn has completed
    When the client requests the turn's context
    Then the response lists every assembled message with its component
    And the components are system, history, rag, mention, user, tools, and thinking
    And the snapshot is retrievable after the turn ends

  Scenario: Auto-retrieved chunks are visible with provenance
    Given a mode with retrieval enabled
    When the engine retrieves chunks before assembly
    Then a rag event carries each chunk's source and score
    And the context snapshot records the same chunks
    And no retrieved chunk enters the payload without appearing in the snapshot

  Scenario: Truncation is labeled, never silent
    Given history or retrieved chunks exceed their budget
    When the assembler truncates
    Then the snapshot records each dropped item with its component and reason
    And the meter's component sum still equals the provider-reported prompt total

  Scenario: Decision outcomes are recorded and metered
    Given the decision layer gates retrieved chunks
    When the gate keeps or drops a chunk
    Then the snapshot records the decision with its score and the deciding model
    And the decision call is metered as its own model row

  Scenario: A degraded decision layer is labeled
    Given the decision service is unreachable
    When the turn runs
    Then the turn proceeds with the ungated context
    And the snapshot labels the decision as degraded with the reason

  Scenario: The snapshot is engine data, not client state
    Given the TUI renders the context panel
    When the user inspects a past turn
    Then the panel renders the persisted snapshot
    And the client computes no provenance, budgets, or drops itself
