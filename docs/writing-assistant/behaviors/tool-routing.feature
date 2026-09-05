# language: en
Feature: Tool routing
  Tool intent is resolved by either the writer (native) or an optional specialist
  router (`ToolDecider`), selected per mode by `toolCalling`.
  Normative per ADR-0028.

  Scenario: Native mode lets the writer decide the exact tool
    Given a mode with toolCalling native and 3 allowlisted tools
    When the writer emits a structured tool_call
    Then the loop invokes the named tool directly
    And no router is consulted

  Scenario: Router mode resolves a request_tool intent
    Given a mode with toolCalling router
    When the writer emits request_tool with an intent string
    Then the loop calls Decide with that intent
    And a confident Decision dispatches Invoke(name, args)

  Scenario: Router refusal degrades gracefully to answering
    Given a mode with toolCalling router
    When Decide returns confidence below the threshold or an empty call
    Then the loop proceeds to answering without a tool
    And no error event is emitted

  Scenario: Router mode requires a served Needle model
    Given a mode with toolCalling router
    And no resolvable needle-router model in the fleet manifest
    When the engine starts
    Then startup fails with mode-refs-router-unavailable

  Scenario: A stale router is refused at startup
    Given a needle-router whose manifest fingerprint differs from the engine tool-set hash
    When the engine starts
    Then startup fails with router-tools-stale

  Scenario: The router call is metered separately
    Given a router-resolved tool call
    When the turn completes
    Then a meter event row is attributed to the router model
    And it is tagged model=needle-router and grouped under the turn's session
