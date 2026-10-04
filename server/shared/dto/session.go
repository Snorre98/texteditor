package dto

import "encoding/json"

// Session is a persisted conversation, doc-level or anchored to a block
// (interface.md §10, ADR-0026). JSON tags are camelCase because the session
// crosses the wire in the API responses and in the `session` feed event
// (ADR-0052 §4).
type Session struct {
	ID            string  `json:"id"`
	DocumentID    string  `json:"documentId"`
	AnchorBlockID *string `json:"anchorBlockId,omitempty"` // nil = doc-level chat; set = selection/bubble anchor
	ModeType      string  `json:"modeType,omitempty"`      // persisted per-session persona
	Title         string  `json:"title,omitempty"`
	TokenBudget   *int    `json:"tokenBudget,omitempty"` // optional per-session cumulative-token cap
	// ContextPolicy is the persisted session-level context policy (ADR-0049 §8)
	// as opaque JSON, or nil/empty when none. The Session store never parses it;
	// the API server and loop unmarshal it.
	ContextPolicy json.RawMessage `json:"contextPolicy,omitempty"`
	CreatedAt     int64           `json:"createdAt,omitempty"`
	UpdatedAt     int64           `json:"updatedAt,omitempty"`
}
