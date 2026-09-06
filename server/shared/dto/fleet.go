package dto

import "encoding/json"

// Capabilities describes what a served model supports (interface.md §1).
// JSON tags are camelCase to match contracts/daemon-http.md §2 — the daemon's
// list verb serializes these verbatim, and the fleet client parses the same shape.
type Capabilities struct {
	ContextLength        int  `json:"contextLength"`
	ThinkingMode         bool `json:"thinkingMode"`
	SupportsSystemPrompt bool `json:"supportsSystemPrompt"`
}

// SamplingParams are the generation parameters passed to a provider
// (interface.md §2). Shared by Provider, Fleet (Resolution.EffectiveParams),
// and Mode (interface.md §0).
type SamplingParams struct {
	Temperature float64
	MaxTokens   int
}

// Model is a discovered/resolved servable model (interface.md §1).
type Model struct {
	Name         string
	BaseURL      string // http://host:port/v1
	Capabilities Capabilities
	ModeTags     []string
}

// Target is an already-resolved serving endpoint handed to the Provider
// (interface.md §2). The Provider never resolves names; it only speaks REST
// to a Target.
type Target struct {
	BaseURL      string
	Capabilities Capabilities
}

// LiveState is the typed serving state of a model (interface.md §1).
type LiveState string

const (
	LiveUp           LiveState = "up"
	LiveDown         LiveState = "down"
	LiveStarting     LiveState = "starting"
	LiveStopping     LiveState = "stopping"
	LiveProvisioning LiveState = "provisioning"
	LiveUnknown      LiveState = "unknown"
)

// ResolveOpts are the inputs to Fleet.Resolve (interface.md §1).
type ResolveOpts struct {
	ModeTag   string          // the mode's name == the fallback tag
	Overrides *SamplingParams // per-call overrides (optional)
}

// Resolution is the result of Fleet.Resolve (interface.md §1).
type Resolution struct {
	Model           Model          // the RESOLVED (possibly fallback) model
	EffectiveParams SamplingParams // merged: manifest.defaults ← mode.params ← overrides
	LiveState       LiveState
	Degraded        bool   // true when a fallback served
	UsedName        string // actual serving name (== fallback when Degraded)
}

// Completion is a non-streaming provider result (interface.md §2).
type Completion struct {
	Text         string
	ToolCalls    []ToolCall // native tool calls when finish_reason == tool_calls
	InputTokens  int        // raw prompt_eval_count
	OutputTokens int        // raw eval_count
}

// RawEvent is an unframed, un-attributed provider event (interface.md §2).
type RawEvent struct {
	Type string          // "token" | "tool_call" | "finish" | "done" | "error"
	Data json.RawMessage // payload shapes per ADR-0016 §2 / interface.md §2:
	//   token     → {"text": "…"}
	//   tool_call → {"id": "…", "name": "…", "arguments": "…"}
	//   finish    → {"reason": "tool_calls" | "stop" | …}
	//   done      → {"inputTokens": n, "outputTokens": n}
	//   error     → {"code": "…", "message": "…"}
}
