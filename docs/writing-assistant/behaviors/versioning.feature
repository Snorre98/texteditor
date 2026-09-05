# language: en
Feature: Document versioning
  Every accepted edit is versioned and revertible, with paragraph-level
  granularity via stable block IDs.
  Normative per ADR-0004, ADR-0020.

  Scenario: Accepted AI edits produce a git commit
    Given a document with an accepted AI edit
    When the Document store commits on accept
    Then a new commit exists with an auto-derived message naming the mode and block

  Scenario: Manual edits autosave on a silence interval
    Given a document the user is typing in directly
    When edits are made with no accept boundary
    Then an autosave snapshot commits on a silence interval, batching many deltas

  Scenario: A paragraph edit is fine-grained, not whole-document
    Given a document where only paragraph P changed
    When the engine computes the diff against the base
    Then only block P is marked changed, via its stable block ID

  Scenario: Block IDs are stable across edits
    Given paragraph P is edited
    When its content is replaced in place
    Then P keeps its block ID, and only split/merge mints new IDs

  Scenario: Word-level diff between two versions
    Given two commits of the same document
    When the client requests the diff
    Then the engine returns word-level insertions and deletions within 100ms

  Scenario: Reverting one edit isolates its blocks
    Given one accepted edit is reverted
    When the revert is applied
    Then only the blocks that edit touched change, plus a new commit
    And adjacent block IDs are untouched

  Scenario: Candidates are unaccepted edits keyed by block ID
    Given three rewrites are proposed for paragraph P
    When the client lists candidates for P
    Then three candidates are diffed against the base, keyed by P's stable block ID
