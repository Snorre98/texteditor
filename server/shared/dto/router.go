package dto

import "encoding/json"

// Decision is a tool-intent resolution from the ToolDecider (interface.md §8b,
// ADR-0028). Additive; only used when the router is enabled.
type Decision struct {
	Name       string          // real tool name (== a ToolDef.Name)
	Args       json.RawMessage // schema-valid arguments for that tool
	Confidence float32         // 0..1; < τ ⇒ "no tool, answer now"
}

// RouterContext is the argument-binding context the loop re-bundles for Decide
// (interface.md §8b).
type RouterContext struct {
	ToolDefs  []ToolDef  // the mode's allowlisted tools (candidate set)
	Chunks    []Chunk    // retrieved chunks (citation/note provenance for args)
	Selection *Selection // the anchored block, when the session is block-scoped
	History   []Message  // recent conversation (arg context)
	UserInput string     // the turn's original request
}

// RouterUsage is the router call's own metering inputs (interface.md §8b).
type RouterUsage struct {
	Breakdown Breakdown      // router prompt's per-component split
	Counts    ProviderCounts // router provider's exact counts
}

// RouterResult is the ToolDecider's outcome (interface.md §8b).
type RouterResult struct {
	Decision Decision
	Usage    RouterUsage
}
