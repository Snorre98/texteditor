# language: en
Feature: Fleet observability
  The model selectors in every client show what serving services are available,
  with the state the daemon reports and the remediation the state implies.
  Normative per ADR-0040 (engine surface) and macos-dev-config ADR-0007
  (daemon batch verb).

  Scenario: One observability read joins the projection with batch states
    Given the daemon serves "mistral-24b" up and "phi-4" down
    When the client calls GET /fleet
    Then the response is control "up" with both models
    And mistral-24b has liveState "up" and phi-4 has liveState "down"
    And the engine issued list and status/all once each — no per-model status calls

  Scenario: A daemon outage is a labeled read, not an error
    Given the engine has previously read the fleet projection
    When the control daemon stops answering
    Then GET /fleet answers 200 with control "unreachable"
    And every model of the last-known projection reports liveState "unknown"
    And the response is distinct from a per-model provider-unreachable

  Scenario: Batch and single status projections never disagree
    Given model "phi-4" was never started
    When the daemon reports its state via status/all and via status/phi-4
    Then both projections report the same state
    And provisioning progress appears only in the single status verb

  Scenario: The model selector renders state and offers the implied action
    Given the selector lists a model reporting "down"
    When the user expands the model's actions
    Then a Start action is offered, routed to POST /models/{name}/start
    And a start refused with model-not-found surfaces the provision hint

  Scenario: Lifecycle actions refresh the selector immediately
    Given the selector shows model "gemma4-26b" as "down"
    When the user starts it
    Then the selector refreshes from /fleet without waiting for the poll
    And gemma4-26b renders "up" only after the daemon reports it

  Scenario: Polling keeps the selector fresh without user action
    Given the selector polls /fleet every 10 seconds while visible
    When an external process stops a model between polls
    Then the next poll renders the daemon's "down" state
    And polling pauses while the window is hidden
