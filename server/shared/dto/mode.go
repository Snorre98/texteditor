package dto

// Mode is a prompt preset: name + system prompt + default model
// (interface.md §8, ADR-0019 as amended by ADR-0045 — behavioral fields removed).
type Mode struct {
	Name         string
	SystemPrompt string
	DefaultModel string
}

// ToolDef is a tool definition + its prompt-spliced function schema
// (interface.md §8).
type ToolDef struct {
	Name        string
	Description string
	Parameters  JSONSchema // prompt-spliced function schema
}
