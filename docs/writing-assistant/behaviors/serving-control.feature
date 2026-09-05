# language: en
Feature: Serving control
  Control what LLMs are served on the machine via the fleet manifest and
  lifecycle verbs, all defined in macos-dev-config.
  Normative per ADR-0006, ADR-0007, ADR-0008, ADR-0018, ADR-0025.

  Scenario: Discover the fleet from the two-tier manifest
    Given a valid two-tier manifest listing daemon "ollama" and model "gemma4-26b"
    When the control daemon serves list
    Then the Fleet gateway receives "gemma4-26b" with endpoint, capabilities, and modeTags
    And no engine code changes when a new model is added to the manifest

  Scenario: Resolve a model name to an endpoint via the daemon
    Given manifest model "mistral-24b" with daemon "mistral-24b" on port 8085
    When the Fleet gateway calls Resolve("mistral-24b", {modeTag: "drafter"})
    Then it returns baseURL "http://127.0.0.1:8085/v1" and contextLength 131072
    And the request went through the control daemon, not a direct manifest read

  Scenario: Folds in fallback and marks it degraded
    Given mode "editor" defaults to "gemma4-26b" which is down
    And another model with modeTag "editor" is up
    When the Fleet gateway calls Resolve("gemma4-26b", {modeTag: "editor"})
    Then the returned Resolution has Degraded=true and UsedName set to the fallback

  Scenario: Start refuses a busy port
    Given daemon "text" on port 8083 already has a listener
    When an operator runs "start text"
    Then start refuses to launch and prints the SERVE_PORT_TEXT override hint

  Scenario: Stop is idempotent
    Given server "text" is down
    When an operator runs "stop text"
    Then stop is a no-op with a warning and does not error

  Scenario: Provision is async and observable
    Given manifest model "gemma4-26b" has source.kind "hf" and a HF repo id
    When an operator runs "provision gemma4-26b"
    Then the daemon returns a provisionID immediately
    And status reports provisioning with bytes/total until complete
    And huggingface-cli downloaded the repo into models/huggingface

  Scenario: Provision honors the lanes rule
    Given model "gemma4-26b" is assigned to daemon "ollama"
    When a second model resolves to the same HF source on daemon "mlx-lm"
    Then the manifest fails validation with lanes-conflict naming both entries

  Scenario: The TUI switches models by starting and stopping servers
    Given the TUI's model switcher selects "gemma4-26b" replacing "gemma4-12b"
    When the user confirms the switch
    Then the Fleet gateway calls Start("gemma4-26b") and Stop("gemma4-12b") via the daemon
    And the completion is not issued until the new server reports up
    And a failed start surfaces as an error event, leaving the old model running

  Scenario: The daemon is the sole manifest reader
    Given both the engine and serve.sh are running
    When a model's capabilities are changed in models.json
    Then only the control daemon re-reads the manifest and propagates the change
    And the engine never reads models.json directly
