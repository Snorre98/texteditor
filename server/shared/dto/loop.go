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

// Task is the input to AgentLoop.Run (interface.md §7, ADR-0026).
type Task struct {
	SessionID  string // the owning session (ADR-0026)
	ModeName   string
	DocumentID string
	UserInput  string
	Selection  *Selection
	Options    *TurnOptions
}
