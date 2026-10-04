package dto

// Request is the fully-assembled provider request (interface.md §2, §5). It is
// produced by the Context assembler — the single choke point (ADR-0011) — and
// carried intact to the Provider gateway, which renders it to the
// OpenAI-compatible wire format. It carries the already-resolved serving model
// name and merged sampling params, so the Provider stays a pure transport: it
// resolves nothing and knows nothing about the fleet.
//
// Introduced at A5 to close the §2/§5 contract gap: the assembled messages and
// tools must reach the Provider, and neither Target nor SamplingParams carries
// them (see interface.md §2 note and ADR-0027).
type Request struct {
	ModelName       string         // the actually-resolved serving model (usedName)
	Messages        []Message      // the assembled conversation (system + history + rag + user)
	Tools           []ToolDef      // all registered tools (global; ADR-0045), in splice order
	EffectiveParams SamplingParams // merged manifest.defaults ← opts.Overrides
	// Thinking is the desired thinking-channel state (ADR-0051 §3): nil leaves
	// the runner default; false asks the runner to disable thinking (the
	// provider maps this per Target.Runner); true asks it to enable thinking
	// (the `auto` escalation). The provider is still a pure transport — it only
	// renders the toggle, never decides the policy.
	Thinking *bool
}
