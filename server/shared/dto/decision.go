package dto

// Laya decision-layer DTOs (ADR-0053). Owner-free pure types: the loop assembles
// a DecisionRecord into the context snapshot, and the Laya client returns these.

// DecisionBreadth is the planner's breadth choice (ADR-0053), mapped to a
// retrieval topK by DecisionPolicy.BreadthTopK.
type DecisionBreadth string

const (
	DecisionBreadthNone DecisionBreadth = "none"
	DecisionBreadthFew  DecisionBreadth = "few"
	DecisionBreadthMany DecisionBreadth = "many"
)

// DecisionDegradeReason labels why a Laya call degraded fail-open (ADR-0053).
type DecisionDegradeReason string

const (
	DecisionDegradedUnreachable DecisionDegradeReason = "unreachable"
	DecisionDegradedTimeout     DecisionDegradeReason = "timeout"
	DecisionDegradedProtocol    DecisionDegradeReason = "protocol"
	DecisionDegradedLowConf     DecisionDegradeReason = "low-confidence"
)

// DecisionPlannerInput is the planner call's input (ADR-0053): the effective
// retrieval query, the bounded recent history, the anchored selection text, and
// the preset name. It never carries mention bodies or the system prompt.
type DecisionPlannerInput struct {
	Request   string
	History   []Message
	Selection string
	Mode      string
}

// DecisionGateInput is the gate call's input (ADR-0053): the request and the
// post-exclude candidate chunks. Threshold is the engine-applied keep τ.
type DecisionGateInput struct {
	Request   string
	Chunks    []Chunk
	Threshold float64
}

// DecisionPlan is the planner call's outcome (ADR-0053). On a transport/timeout/
// protocol failure it is returned with Degraded=true and a Reason; the loop then
// falls back to policy defaults. Checkpoint is Laya's routed checkpoint.
type DecisionPlan struct {
	Checkpoint       string                `json:"checkpoint,omitempty"`
	Retrieve         bool                  `json:"retrieve"`
	Thinking         ThinkingLevel         `json:"thinking,omitempty"`
	Breadth          DecisionBreadth       `json:"breadth,omitempty"`
	Degraded         bool                  `json:"degraded"`
	Reason           DecisionDegradeReason `json:"reason,omitempty"`
	PromptTokens     int                   `json:"promptTokens,omitempty"`
	CompletionTokens int                   `json:"completionTokens,omitempty"`
}

// DecisionChunkResult is one candidate chunk's gate decision (ADR-0053). Keep is
// score >= threshold (or a low-confidence fail-open keep); Score is P(relevant).
type DecisionChunkResult struct {
	ChunkKey string  `json:"chunkKey"`
	Path     string  `json:"path,omitempty"`
	Score    float64 `json:"score"`
	Keep     bool    `json:"keep"`
	Reason   string  `json:"reason,omitempty"`
}

// DecisionGateResult is the gate call's outcome (ADR-0053). On failure it is
// returned with Degraded=true and no chunk decisions; the loop then keeps all
// chunks (fail-open).
type DecisionGateResult struct {
	Checkpoint       string                `json:"checkpoint,omitempty"`
	Threshold        float64               `json:"threshold"`
	Degraded         bool                  `json:"degraded"`
	Reason           DecisionDegradeReason `json:"reason,omitempty"`
	Chunks           []DecisionChunkResult `json:"chunks,omitempty"`
	PromptTokens     int                   `json:"promptTokens,omitempty"`
	CompletionTokens int                   `json:"completionTokens,omitempty"`
}

// DecisionRecord is the engine-owned record of the Laya decision layer for one
// turn (ADR-0053), persisted in the context snapshot. It explains retrieve-or-
// not, the thinking level, and every per-chunk gate outcome. Present only when
// the layer ran (or degraded) on the turn.
type DecisionRecord struct {
	Enabled  bool                  `json:"enabled"`
	Degraded bool                  `json:"degraded"`
	Reason   DecisionDegradeReason `json:"reason,omitempty"`
	Planner  *DecisionPlan         `json:"planner,omitempty"`
	Gate     *DecisionGateResult   `json:"gate,omitempty"`
}
