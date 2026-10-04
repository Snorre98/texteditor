# language: en
Feature: Locate a pasted chunk and edit exactly there
  A `/locate` line followed by pasted text resolves the chunk to a document and
  block in the vault; the turn is anchored to that block, the model returns only
  the replacement, and the approved edit is guarded and written through.
  Normative per ADR-0048, ADR-0036, ADR-0029, ADR-0047.

  Scenario: An exact paste resolves to its block
    Given the open document contains the pasted paragraph verbatim
    When the turn starts with /locate followed by that paragraph
    Then the engine resolves the anchor to that block with matchType exact
    And a locate event reports the document, path, and block id
    And the context snapshot records the locate outcome

  Scenario: Whitespace and markdown differences still resolve
    Given the pasted text differs from the source only in whitespace or markdown markers
    When the engine normalizes and matches
    Then the anchor resolves to the same block

  Scenario: Fuzzy matches require confirmation
    Given no exact normalized match exists
    And a block scores above the fuzzy threshold
    When the engine returns the ranked candidates
    Then the turn waits for the user to pick a candidate
    And no edit is attempted before the choice

  Scenario: Not found degrades to plain chat
    Given no candidate scores above the threshold
    When the engine reports locate-not-found
    Then the turn proceeds as normal chat with a labeled warning
    And no anchor is set

  Scenario: The anchored turn is block-scoped
    Given a resolved anchor
    When the model streams its replacement
    Then the context includes the anchored block and its neighbors
    And the edit targets the anchored block with its base-hash guard
    And an approving user writes through per ADR-0047

  Scenario: Locating costs no model tokens
    Given a /locate turn
    When the resolver runs
    Then matching is engine-side and deterministic
    And the meter reports no model call for the locate step
