# language: en
Feature: Edit integrity
  Edits are whole-block replacements; the engine owns formatting; a block-level
  guard detects stale context and a structured result informs retries.
  Normative per ADR-0029.

  Scenario: An edit replaces a whole block by stable ID
    Given a document with block P
    When edit_markdown supplies the complete new content of P
    Then only P's content changes
    And no neighbor block is touched

  Scenario: The model's whitespace is normalized, not trusted
    Given an edit whose text uses inconsistent indentation
    When ApplyEdit stages the candidate
    Then the text is normalized to canonical form
    And the result reports what was normalized

  Scenario: A structurally invalid table is rejected with issues
    Given an edit whose table separator row has fewer columns than its header
    When the edit-tool handler validates the text
    Then the edit is rejected with invalid-structure
    And the issues name the offending line and reason

  Scenario: A stale context is caught by the guard
    Given an edit carrying a guard over sibling block Y
    And Y's canonical content no longer matches the echoed hash
    When ApplyEdit verifies the guard
    Then the edit is rejected with guard-failed naming block Y

  Scenario: Opinionated formatting runs on accept and save
    Given an accepted candidate or an autosaved manual edit
    When the content is committed
    Then it is formatted to the hardcoded opinionated style
    And the formatting is visible as a reviewable diff

  Scenario: Content hashes are stable because content is canonical
    Given blocks stored in canonical form
    When the engine computes a block's content hash
    Then the same content always yields the same hash
