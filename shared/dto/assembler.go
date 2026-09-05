package dto

// AssemblerInput is the input to ContextAssembler.Assemble (interface.md §5).
type AssemblerInput struct {
	Mode      Mode
	ModelName string         // the actually-resolved serving model (usedName)
	Params    SamplingParams // merged effective params (impact the rendered request)
	Tools     []ToolDef      // the mode's allowlisted tools, in splices order
	RAGChunks []Chunk
	History   []Message
	UserInput string
}

// Breakdown is the deterministic per-component token approximation, in a
// documented unit (interface.md §5).
type Breakdown struct {
	SystemPrompt, Tools, Rag, History, User, Thinking int
}

// Payload is the assembled request (interface.md §5).
type Payload struct {
	Messages []Message // the assembled message list (system + history + rag + user)
	Request  Request   // the provider-ready request handed verbatim to the Provider
}

// ProviderCounts are the raw provider-reported counts (interface.md §6).
type ProviderCounts struct {
	InputTokens    int // prompt_eval_count
	OutputTokens   int // eval_count
	ThinkingTokens int // reasoning count if reported; 0 if omitted
}

// AttributedBreakdown is the breakdown scaled to exact provider totals
// (interface.md §6).
type AttributedBreakdown struct {
	SystemPrompt, Tools, Rag, History, User, Thinking int  // scaled to exact totals
	ThinkingApprox                                    bool // true when thinking was tokenized (ADR-0024)
}
