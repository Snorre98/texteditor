package dto

// ContextBudget bounds the history, RAG, and mention tokens a mode may assemble
// (interface.md §8; MaxMentionTokens added by ADR-0036 §4).
type ContextBudget struct {
	MaxHistoryTokens int
	MaxRagTokens     int
	MaxMentionTokens int
}

// Mode is a declarative persona: prompt + default model + tool set + budget
// (interface.md §8, ADR-0019).
type Mode struct {
	Name          string
	SystemPrompt  string
	DefaultModel  string
	ToolAllowlist []string
	Params        SamplingParams
	ContextBudget ContextBudget
	MaxSteps      int    // per-mode bound on dispatch/observe (default from policy)
	Agentic       bool   // multi-turn tool loop vs single-shot pass
	Kind          string // "model" | "assistant" (reserved)
	Preamble      string // spliced before systemPrompt (e.g. citation line)
	ToolCalling   string // "native" | "router" (default "native") — ADR-0028
}

// ToolDef is a tool definition + its prompt-spliced function schema
// (interface.md §8).
type ToolDef struct {
	Name        string
	Description string
	Parameters  JSONSchema // prompt-spliced function schema
}
