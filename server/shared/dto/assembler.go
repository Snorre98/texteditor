package dto

// MentionContent is a turn-scoped, resolved mention attachment (interface.md §5,
// ADR-0036 §3).
type MentionContent struct {
	Path string
	Text string // raw content, truncated by PipelinePolicy.MaxMentionTokens
}

// AssemblerInput is the input to ContextAssembler.Assemble (interface.md §5).
type AssemblerInput struct {
	Mode      Mode
	ModelName string         // the actually-resolved serving model (usedName)
	Params    SamplingParams // merged effective params (impact the rendered request)
	Tools     []ToolDef      // all registered tools (global; ADR-0045 §2), in splice order
	Policy    PipelinePolicy // the one global pipeline policy (budgets; ADR-0045 §3)
	RAGChunks []Chunk
	History   []Message
	Mentions  []MentionContent // spliced after history, before user input (ADR-0036)
	UserInput string
}

// Breakdown is the deterministic per-component token approximation, in a
// documented unit (interface.md §5). Mentions added by ADR-0036 §3.
type Breakdown struct {
	SystemPrompt, Tools, Rag, History, Mentions, User, Thinking int
}

// Payload is the assembled request (interface.md §5). Provenance, Drops, and
// Budget are the ADR-0044 §4 context-inspector additions: every assembled
// message's component/source, the labeled truncation records, and per-component
// budget utilization. The assembler stays pure — it never knows about sessions,
// workspaces, or snapshots; the loop wraps these into a ContextSnapshot.
type Payload struct {
	Messages   []Message           // the assembled message list (system + history + rag + user)
	Request    Request             // the provider-ready request handed verbatim to the Provider
	Provenance []MessageProvenance // component + provenance for every assembled message
	Drops      []ContextDrop       // labeled truncation/drop records (never silent)
	Budget     []BudgetUsage       // per-component utilization vs the PipelinePolicy limit
}

// MessageProvenance is one assembled message's component and provenance
// (ADR-0044 §4). Component ∈ system | history | rag | mention | user. Source
// names the file path for rag/mention messages (empty otherwise); Tokens is the
// deterministic estimate contributing to Breakdown; Pinned is a human override
// (Phase C4; always false in C3).
type MessageProvenance struct {
	Role      string
	Component string
	Source    string
	Tokens    int
	Pinned    bool
}

// ProviderCounts are the raw provider-reported counts (interface.md §6).
type ProviderCounts struct {
	InputTokens    int // prompt_eval_count
	OutputTokens   int // eval_count
	ThinkingTokens int // reasoning count if reported; 0 if omitted
}

// AttributedBreakdown is the breakdown scaled to exact provider totals
// (interface.md §6). Mentions added by ADR-0036 §5.
type AttributedBreakdown struct {
	SystemPrompt, Tools, Rag, History, Mentions, User, Thinking int  // scaled to exact totals
	ThinkingApprox                                              bool // true when thinking was tokenized (ADR-0024)
}

// SessionMeterComponent is one component's cumulative meter for a session
// (interface.md §6). Approx labels a component accumulated from labeled
// approximations (thinking, ADR-0024).
type SessionMeterComponent struct {
	Component        string `json:"component"` // system|tools|rag|history|mentions|user|thinking|completion
	PromptTokens     int    `json:"promptTokens"`
	CompletionTokens int    `json:"completionTokens"`
	Approx           bool   `json:"approx,omitempty"`
}

// SessionMeter is a session's cumulative per-component token meter
// (interface.md §6, ADR-0026 §5).
type SessionMeter struct {
	SessionID  string                  `json:"sessionId"`
	Components []SessionMeterComponent `json:"components"`
	Total      int                     `json:"total"` // cumulative prompt + completion across components
}
