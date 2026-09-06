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

// Revision is a versioned checkpoint of a document (interface.md §9).
type Revision struct {
	ID        string
	Message   string
	Timestamp int64
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
