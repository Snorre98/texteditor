package dto

import "encoding/json"

// Session is a persisted conversation, doc-level or anchored to a block
// (interface.md §10, ADR-0026).
type Session struct {
	ID            string // UUID, client-facing identity
	DocumentID    string
	AnchorBlockID *string // nil = doc-level chat; set = selection/bubble anchor
	ModeType      string  // persisted per-session persona
	Title         string
	TokenBudget   *int // optional per-session cumulative-token cap
	// ContextPolicy is the persisted session-level context policy (ADR-0049 §8)
	// as opaque JSON, or nil/empty when none. The Session store never parses it;
	// the API server and loop unmarshal it.
	ContextPolicy json.RawMessage
	CreatedAt     int64
	UpdatedAt     int64
}
