package dto

import "encoding/json"

// Event is a typed SSE event on the bus (interface.md §11).
type Event struct {
	TurnID string // correlation id (empty for non-turn events)
	Type   string // token|meter|candidate|diff|done|error|backpressure
	Data   json.RawMessage
}
