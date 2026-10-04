package dto

import "encoding/json"

// Event is a typed SSE event on the bus (interface.md §11).
type Event struct {
	TurnID string // correlation id (empty for non-turn events)
	// WorkspaceID scopes a non-turn feed event to a workspace (ADR-0052 §4).
	// Empty = global (always delivered to a filtered feed). Not on the wire;
	// the bus uses it to filter `GET /events?workspaceId=`.
	WorkspaceID string
	Type        string // turn|token|meter|candidate|diff|done|error|backpressure|corpus|document|session|fleet
	Data        json.RawMessage
}

// Non-turn liveness feed event types (ADR-0052 §4).
const (
	EventCorpus   = "corpus"
	EventDocument = "document"
	EventSession  = "session"
	EventFleet    = "fleet"
)

// CorpusEventPayload is the wire payload of a `corpus` feed event (ADR-0052 §4).
// Workspace-scoped: delivered only to a feed filtered to WorkspaceID.
type CorpusEventPayload struct {
	WorkspaceID string    `json:"workspaceId"`
	Job         CorpusJob `json:"job"`
}

// DocumentEventPayload is the wire payload of a `document` feed event
// (ADR-0052 §4). Global; always delivered.
type DocumentEventPayload struct {
	Kind       string `json:"kind"` // external-change | commit
	DocumentID string `json:"documentId"`
	Path       string `json:"path"`
}

// SessionEventPayload is the wire payload of a `session` feed event
// (ADR-0052 §4). Workspace-scoped.
type SessionEventPayload struct {
	Kind        string  `json:"kind"` // created | renamed
	WorkspaceID string  `json:"workspaceId"`
	Session     Session `json:"session"`
}

// FleetEventPayload is the wire payload of a `fleet` feed event (ADR-0052 §4).
// Global; always delivered. It mirrors the /fleet projection (control + models)
// so a client renders the same shape from the feed and from GET /fleet.
type FleetEventPayload struct {
	Control string            `json:"control"` // up | unreachable
	Models  []FleetEventModel `json:"models"`
}

// FleetEventModel is one model row of a `fleet` feed event. It is the
// client-facing projection (never the internal dto.Model, which carries
// Runner/ModelID that must not be exposed — ADR-0016 §1).
type FleetEventModel struct {
	Name         string       `json:"name"`
	BaseURL      string       `json:"baseUrl"`
	Capabilities Capabilities `json:"capabilities"`
	ModeTags     []string     `json:"modeTags,omitempty"`
	LiveState    LiveState    `json:"liveState"`
}
