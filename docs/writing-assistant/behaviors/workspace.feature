# language: en
Feature: Workspace navigation and file mentions
  `texteditor .` opens the TUI on a directory; the file listing is served by
  the engine over the contract, and `@filename` in chat attaches the file's
  content to the turn as metered context.
  Normative per ADR-0035, ADR-0036.

  Scenario: The listing is engine-served, never client-read
    Given the TUI holds a workspace directory
    When the TUI needs the file listing
    Then it calls GET /directories with the directory's absolute path
    And the entries come from the engine's Workspace module
    And the TUI reads the filesystem at no point

  Scenario: Opening the TUI on a directory lists it
    Given the TUI is launched with a directory argument
    When the workspace state loads
    Then the picker shows the entries returned for that directory

  Scenario: Listing is shallow and non-recursive
    Given a directory containing files and a subdirectory
    When the directory is listed
    Then every entry is a direct child of the directory
    And the subdirectory appears as one entry with isDir true
    And no entries from inside the subdirectory appear

  Scenario: A mention attaches file content as context
    Given the user types @ and picks a file from the listing
    When the turn is submitted
    Then the turn request carries the file's absolute path in mentions
    And the engine reads the content through the Workspace module
    And the assembled context contains the content marked with its path

  Scenario: A mentioned file is never a versioned document
    Given a turn with a mentioned file
    When the turn runs
    Then the mentioned file gets no surrogate document id
    And its git history is unchanged
    And only the active document is versioned

  Scenario: Mention failures are typed and fail the turn before streaming
    Given a turn whose mention path does not resolve
    When the engine resolves the mentions
    Then the turn ends with an error event carrying code mention-not-found
    And no model stream starts

  Scenario: The mention cost is visible in the meter
    Given a turn with one mentioned file
    When the context assembler builds the payload
    Then the breakdown reports a non-negative mentions component
    And the meter event carries mentions
    And the component sum equals the provider-reported prompt total
