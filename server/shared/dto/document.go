package dto

// TextFormatterIssue is a structural problem found by TextFormatter.Validate
// (interface.md §4b).
type TextFormatterIssue struct {
	Line    int
	Message string
}

// Guard is a block-level context guard an edit relies on (interface.md §9,
// ADR-0029 §4).
type Guard struct {
	BlockID string // a sibling/context block the edit relies on
	Hash    string // short hash of its canonical content
}

// BlockEdit is a whole-block replacement proposal (interface.md §9).
type BlockEdit struct {
	BlockID string
	Text    string
	Guards  []Guard // optional block-level context guards (ADR-0029)
	Mode    string  // preset that produced the edit (ADR-0020 §1); internal, not on the HTTP wire
}

// BlockWrite is one block in a manual whole-tree save (interface.md §9,
// ADR-0038). ID is nil for a new block (the engine mints a UUID, ADR-0020 §3);
// a changed Kind/ParentID retypes/moves. No hash/guards — those are AI-edit
// concerns (ADR-0029 §4). Array order in a tree is position.
type BlockWrite struct {
	ID       *string
	ParentID *string
	Kind     BlockKind
	Text     string
}

// Revision is a versioned checkpoint of a document (interface.md §9). The
// write-boundary fields (ADR-0047 §8) report the write-through outcome.
type Revision struct {
	ID             string
	Message        string
	Timestamp      int64
	WrittenThrough bool
	Path           string
}

// OpenResult is the outcome of opening a document by path (ADR-0047 §2). Path is
// the canonical (symlink-resolved) path the document row is keyed by;
// ExternalChange is true when the disk file differed from the engine's
// last-known hash and was re-read into the worktree.
type OpenResult struct {
	DocumentID     string
	Path           string
	ExternalChange bool
}

// WriteResult is the outcome of a write boundary (Commit / SaveTree, ADR-0047
// §8): the resulting revision, whether the opened file was mirrored, the path
// mirrored, and whether a commit was created (false for an empty accept or an
// engine-only no-op).
type WriteResult struct {
	Revision       Revision
	WrittenThrough bool
	Path           string
	Committed      bool
}

// SaveOptions are the explicit save flags (ADR-0039 / ADR-0047 §3).
type SaveOptions struct {
	WriteThrough bool // mirror canonical markdown to the opened file
	Overwrite    bool // explicit opt-in over an external change
}

// CommitOptions are the accept flags (ADR-0047 §3).
type CommitOptions struct {
	Overwrite bool // explicit opt-in over an external change
}

// Candidate is an unaccepted AI edit, keyed by block ID (interface.md §9).
type Candidate struct {
	BlockID string
	Text    string
	BaseID  string // the base revision it's diffed against
}

// WordEdit is a word-level diff of one block between two revisions
// (interface.md §9).
type WordEdit struct {
	BlockID    string
	Insertions []string
	Deletions  []string
}
