# language: en
Feature: Token metering
  Every model call reports a per-component token breakdown, attributed from the
  provider's own counts.
  Normative per ADR-0011, ADR-0016, ADR-0022, ADR-0024.

  Scenario: Breakdown attributes each component
    Given a turn using mode "proofreader" with 2 tools and 3 RAG chunks
    When the context assembler builds the payload
    Then the breakdown reports non-negative tokens for system, tools, rag, history, and user
    And the meter scales the components so the sum equals the provider-reported prompt_eval_count

  Scenario: The meter moves when a lever changes
    Given a turn with 3 tool schemas in context
    When a fourth tool is added to the mode's allowlist
    Then the next turn's tools-component token count increases
    And the increase is visible as a live meter event

  Scenario: Breakdown is rendered within the Q1 response-measure
    Given the provider's usage has landed
    When the meter attributes and emits
    Then the breakdown is visible to the client within 100 milliseconds

  Scenario: Thinking tokens are surfaced separately
    Given a model with thinkingMode true that reports reasoning tokens
    When the provider reports thinking tokens in the completion
    Then they are attributed to the thinking component, not the completion total

  Scenario: Omitted thinking tokens are approximated and labeled
    Given a thinking model whose runner omits the reasoning count
    When the meter reconciles the thinking component
    Then it tokenizes the reasoning prefix with a bundled tokenizer
    And the result is marked approx so the approximation is never silent

  Scenario: Empty retrieval is honest, not silent
    Given the retriever returns zero chunks
    When the context assembler builds the payload
    Then the breakdown reports rag 0 and the turn proceeds without RAG

  Scenario: The meter owns attribution, not fan-out
    Given a turn completes
    When the meter persists and emits
    Then the meter writes meter.db and emits one meter event to the bus
    And subscription/fan-out is the SSE bus's concern, not the meter's
