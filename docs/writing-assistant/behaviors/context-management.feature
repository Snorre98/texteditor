# language: en
Feature: Context management — workspaces, multi-root corpus, and the turn context tray
  A workspace is a persistent engine entity: a root directory plus its
  subdirectories, governing browsing and editing only. Its corpus is an
  independent, multi-root set of files and directories with include/exclude
  globs, governing retrievability only. Context state — index, sessions,
  meter — lives in a per-workspace shard; documents and their git history stay
  global, so one canonical path keeps one document identity. The context tray
  shows the assembled components with per-item pin/remove, an editable
  retrieval query, and an auto-RAG toggle; the engine re-assembles and
  re-meters after every edit, and pins are human overrides that bypass the
  decision gate but never budgets. Browsing and indexing are bounded by
  ALLOWED_ROOTS.
  Normative per ADR-0049, ADR-0011, ADR-0036, ADR-0044, ADR-0048, ADR-0052.

  Scenario: Opening a directory creates or resumes its workspace
    Given the author opens a directory
    When the engine resolves the workspace
    Then the directory is registered as a workspace root and gets a storage shard
    And the workspace's corpus scope and sessions are restored on resume
    And opening a nested directory resolves to the most specific existing workspace

  Scenario: Workspace context state is isolated in its shard
    Given two workspaces with their own shards
    When a session is created in the first workspace
    Then the session never appears in the second workspace
    And rebuilding the first workspace's index leaves the second's index untouched
    And document identity and git history remain global

  Scenario: Retrieval spans the union of corpus roots
    Given a workspace whose corpus roots include a directory outside the workspace root
    When a retrieval runs
    Then chunks from every included root can be returned
    And a file reachable via two roots is indexed once
    And the workspace root itself never bounds what is retrievable

  Scenario: Default corpus scope indexes markdown and excludes hidden directories
    Given a corpus with no explicit scope beyond its roots
    When the corpus is indexed
    Then markdown documents under the roots are indexed
    And files under hidden directories such as .obsidian are not indexed

  Scenario: Corpus scope decides what is retrievable
    Given a document is outside the corpus scope
    When a retrieval runs
    Then no chunk from that document appears in the results
    And the context snapshot shows no chunk from that document

  Scenario: Scope changes are index-only
    Given a corpus with indexed documents
    When the author changes the include/exclude scope or the corpus roots
    Then the index is reconciled accordingly
    And no document file on disk is created, modified, or deleted

  Scenario: Excluding a document evicts it from retrieval
    Given an indexed document
    When the scope is changed to exclude it
    Then its chunks are no longer retrievable
    And re-applying the same scope changes nothing

  Scenario: Eviction removes both vector and FTS rows and is idempotent
    Given an indexed document with vector and FTS rows
    When the document is evicted twice
    Then neither call errors
    And the document appears in neither vector search nor full-text search

  Scenario: Per-document index status is visible
    Given an indexed document and a document whose file changed since indexing
    When the client reads the corpus status
    Then the first reports indexed with its chunk count
    And the second reports stale

  Scenario: Browsing and indexing are bounded by ALLOWED_ROOTS
    Given an engine with an ALLOWED_ROOTS allowlist
    When a path outside the allowlist is browsed or claimed as a corpus root
    Then the engine refuses it with a typed path-outside-allowed-roots error
    And the refusal holds even when the engine is bound to 0.0.0.0

  Scenario: Removing a chunk in the tray keeps it out of the payload
    Given an auto-retrieved chunk is shown in the context tray
    When the author removes it before the turn is sent
    Then the assembled payload does not contain that chunk
    And the context snapshot records the removal

  Scenario: Pinned context persists for the session and bypasses the gate
    Given the author pins an item for the session
    And the decision layer is enabled
    When the next turn is assembled
    Then the pinned item is included without a gate decision
    And auto-retrieved chunks are still gated
    And the snapshot labels the pinned item as a human override

  Scenario: Budgets and truncation apply after overrides and stay labeled
    Given a tray whose pinned and included items exceed the context budget
    When the engine re-assembles the turn
    Then truncation applies to the assembled set
    And every dropped item is labeled in the snapshot
    And the meter's component sum still equals the provider-reported prompt total

  Scenario: An ambiguous locate outcome feeds the tray
    Given a /locate turn with multiple candidates above the fuzzy threshold
    When the engine returns the ranked candidates
    Then the tray shows the candidates and the turn waits for a choice
    And the chosen anchor appears in the assembled context once selected
    And no edit is attempted before the choice

  Scenario: A not-found locate outcome is labeled in the tray
    Given no candidate scores above the threshold
    When the engine reports locate-not-found
    Then the tray shows the labeled warning and no anchor
    And the turn proceeds as plain chat
    And the snapshot records the not-found outcome

  Scenario: The TUI sends decisions; the engine assembles
    Given the TUI shows the corpus tree and the context tray
    When the author toggles scope, pins, removes an item, or edits the retrieval query
    Then the client sends decisions, not payload text
    And the engine assembles and meters the resulting payload
    And the client computes no tokens, budgets, or provenance itself

  Scenario: Corpus progress arrives on the liveness feed
    Given the client is subscribed to GET /events
    When the author starts a corpus index or the scope changes
    Then job progress and completion arrive as typed corpus events
    And the client does not poll GET /corpus for progress
