# language: en
Feature: Reasoning policy and context budgets
  Thinking is a policy, not a default: mechanical turns run thinking-off, `auto`
  escalates only after a structured failure, and the thinking that does happen
  is counted exactly. Context is bounded per turn by the model window and per
  session by a soft/hard budget with metered compaction — every drop and every
  escalation is a labeled record, never silent.
  Normative per ADR-0051, ADR-0011, ADR-0024, ADR-0045, ADR-0049.

  Scenario: Mechanical turns run thinking-off
    Given the session thinking policy is off or auto
    When a mechanical edit turn runs
    Then the provider request disables the runner's thinking channel
    And the turn is not charged thinking tokens

  Scenario: Auto escalates only after a structured failure
    Given the session thinking policy is auto
    And the first attempt fails with invalid-structure or guard-failed
    When the engine retries
    Then the retry runs with thinking enabled
    And the snapshot labels the escalation with the failure that triggered it
    And no more than one escalation happens per turn

  Scenario: A runner that cannot disable thinking degrades with a label
    Given the resolved runner has no thinking toggle
    When a thinking-off turn runs
    Then the turn proceeds with thinking enabled
    And the snapshot labels the result thinking-unsupported

  Scenario: Thinking tokens are metered exactly
    Given a turn whose provider streams reasoning deltas
    When the meter attributes the turn
    Then the thinking component equals the provider's reasoning tokens
    And the component sum still equals the provider-reported prompt total

  Scenario: Truncated thinking is a labeled result, not an empty turn
    Given the thinking budget is reached before a tool call or answer
    When the turn ends
    Then the snapshot labels the result thinking-truncated
    And the client receives a labeled terminal event, never an empty done

  Scenario: The per-turn window gate drops with labels
    Given the assembled payload exceeds the model's context window
    When the assembler applies the window budget
    Then it drops the lowest-priority components first (oldest history, then RAG,
        then recent history)
    And every drop is recorded with the context-window reason
    And the payload plus the output reserve fits the window

  Scenario: Fixed and pinned context over the window is a typed refusal
    Given system, tools, user input, and pinned items alone exceed the window
    When the turn is requested
    Then the engine refuses with context-window-exceeded before any provider call
    And no partial payload is sent

  Scenario: A session soft budget warns and proceeds
    Given a session token budget with a soft threshold
    When cumulative usage crosses the soft threshold
    Then the turn proceeds
    And the snapshot labels the warning session-budget-soft

  Scenario: A session hard budget refuses or compacts
    Given a session token budget with a hard threshold
    When cumulative usage would cross the hard threshold
    Then the turn is refused with session-budget-exceeded
    Unless compaction is enabled and succeeds first

  Scenario: Compaction replaces oldest history with a metered summary
    Given history exceeds the compaction trigger
    When the engine compacts
    Then the oldest turns are replaced by one labeled summary message
    And the summary call is metered as its own model row
    And pinned items and the most recent turns survive
    And the snapshot records the compacted turn range

  Scenario: Compaction's prefix cost is labeled
    Given compaction or a tray edit changes the front-loaded prefix
    When the next turn is assembled
    Then the snapshot records that prefix-cache reuse was traded away
    And the cost is visible, not silent

  Scenario: Turn measurements are recorded per model
    Given a completed turn
    When the meter and snapshot are persisted
    Then they record prompt tokens, thinking tokens, completion tokens,
        wall-clock latency, model and quant, and window utilization
    And no measurement is approximated without a label
