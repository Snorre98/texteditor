# language: en
Feature: Provider hot-swap and fallback
  Swap which model serves a mode without an engine rebuild, and degrade
  gracefully when the preferred model is unavailable.
  Normative per ADR-0005, ADR-0009, ADR-0015, ADR-0016.

  Scenario: Swap a mode's model by editing data
    Given mode "editor" defaults to model "gemma4-12b"
    When the mode definition's defaultModel is changed to "gemma4-26b"
    Then the next turn resolves and calls "gemma4-26b" with no engine rebuild

  Scenario: Resolve time-loads modes and validates fail-fast
    Given a mode references defaultModel "missing-model"
    When the engine starts
    Then startup fails with mode-refs-unknown-model naming the mode and model

  Scenario: Fall back to a lower-tier model in the same tag
    Given mode "editor" defaults to "gemma4-26b" which is down
    And another model with modeTag "editor" is up
    When the agent loop runs a turn
    Then Fleet.Resolve selects the fallback and marks the Resolution Degraded=true
    And the done event labels degraded=true with usedModel

  Scenario: No model available is surfaced, not silent
    Given mode "drafter" has no servable model with its modeTag
    When the agent loop runs a turn
    Then it emits an error event with code no-model-available
    And the token meter shows the turn produced no completion

  Scenario: Citation floor is enforced by capability gate
    Given a model below the 14B citation floor
    When the fleet manifest is validated against the policy
    Then the model is tagged grammar-only and cannot be selected by a drafting mode

  Scenario: The provider never resolves names
    Given the agent loop asks the provider to stream
    When the provider receives a Target
    Then the provider speaks REST with no knowledge of model names or the manifest
