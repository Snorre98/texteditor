package dto

// Session is a persisted conversation, doc-level or anchored to a block
// (interface.md §10, ADR-0026).
type Session struct {
	ID            string // UUID, client-facing identity
	DocumentID    string
	AnchorBlockID *string // nil = doc-level chat; set = selection/bubble anchor
	ModeType      string  // persisted per-session persona
	Title         string
	TokenBudget   *int // optional per-session cumulative-token cap
	CreatedAt     int64
	UpdatedAt     int64
}
