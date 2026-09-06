package dto

// Selection anchors a turn to a block (the "edit this paragraph" popover)
// (interface.md §7).
type Selection struct {
	BlockID string
}

// TurnOptions are per-turn overrides (interface.md §7).
type TurnOptions struct {
	Temperature *float64
	Model       string // force a model for this turn (optional)
}

// Mention is a turn-scoped, client-resolved file attachment (interface.md §7,
// ADR-0036 §1).
type Mention struct {
	Path string // absolute path; resolved by the client (workspace-relative → absolute)
}

// Task is the input to AgentLoop.Run (interface.md §7, ADR-0026).
type Task struct {
	SessionID  string // the owning session (ADR-0026)
	ModeName   string
	DocumentID string
	UserInput  string
	Selection  *Selection
	Mentions   []Mention // turn-scoped context attachments (ADR-0036); never persisted
	Options    *TurnOptions
}
