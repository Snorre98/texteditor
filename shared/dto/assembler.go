package dto

import "encoding/json"

// AssemblerInput is the input to ContextAssembler.Assemble (interface.md §5).
type AssemblerInput struct {
	Mode        Mode
	ToolSchemas []JSONSchema
	RAGChunks   []Chunk
	History     []Message
	UserInput   string
}

// Breakdown is the deterministic per-component token approximation, in a
// documented unit (interface.md §5).
type Breakdown struct {
	SystemPrompt, Tools, Rag, History, User, Thinking int
}

// Payload is the assembled request body (interface.md §5).
type Payload struct {
	Messages []Message       // the assembled request body
	Request  json.RawMessage // provider-ready request (undocumented internals stay private)
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
	SystemPrompt, Tools, Rag, History, User, Thinking int // scaled to exact totals
	ThinkingApprox                                   bool // true when thinking was tokenized (ADR-0024)
}
