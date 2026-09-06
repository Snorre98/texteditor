// Package tooldecider holds the ToolDecider — the optional tool-routing service
// (ADR-0028, interface.md §8b): in router mode the writer emits `request_tool`
// with a free-text intent, and the decider resolves "which tool, what arguments"
// against the router model. It is NOT a leaf (Retriever-style, ADR-0016 §8): it
// resolves needle-router via Fleet and calls Provider internally.
//
// The prompt layout, the confidence threshold τ (default 0.7), and the router
// facade's completion protocol are hidden internals:
//
//   - a confident decision is a non-streaming completion with
//     FinishReason == "tool" whose content is compact JSON
//     {"name","args","confidence"};
//   - a refusal is an empty completion (FinishReason "stop");
//   - τ is applied HERE: Decide returns a Decision with Name != "" only when the
//     reported confidence is ≥ τ (interface.md §8b recorded amendment).
package tooldecider

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"texteditor/shared/dto"
)

// RouterModelName is the router model the decider resolves via Fleet, by name
// (the nomic-embed special-purpose pattern, ADR-0028 §7).
const RouterModelName = "needle-router"

// defaultThreshold is the private confidence threshold τ (ADR-0028 §1).
const defaultThreshold = 0.7

// routerInstruction is the private router prompt's instruction component
// (ADR-0028 §5 maps it to Breakdown.SystemPrompt).
const routerInstruction = "You are a tool-calling router. Choose the single most relevant tool from the candidate tools below for the user's request and produce its arguments. Reply with ONLY JSON: {\"name\": \"<tool name>\", \"args\": {<tool arguments>}, \"confidence\": <0.0-1.0>}. If no tool is needed, reply with an empty response."

// ToolDecider is the ToolDecider public API (interface.md §8b).
type ToolDecider interface {
	SignalTool() dto.ToolDef
	Decide(ctx context.Context, intent string, c dto.RouterContext) (dto.RouterResult, error)
}

// Interface is an alias for ToolDecider (the contracted name, interface.md §8b).
type Interface = ToolDecider

// Resolver is the sealed subset of the Fleet gateway the decider needs: it
// resolves the router model to a served Target (interface.md §1). Wired to the
// real Fleet gateway at the composition root.
type Resolver interface {
	Resolve(name string, opts dto.ResolveOpts) (dto.Resolution, error)
}

// Chatter is the sealed subset of the Provider gateway the decider needs: one
// non-streaming completion (ADR-0028 §7 — a small response, zero Provider
// change). Wired to the real Provider gateway at the composition root.
type Chatter interface {
	Chat(ctx context.Context, target dto.Target, req dto.Request) (dto.Completion, error)
}

// decider is the concrete ToolDecider. Stateless except its seams.
type decider struct {
	resolver Resolver
	chatter  Chatter
}

// New returns a ToolDecider over the Fleet resolver and the Provider chatter
// (both sealed subsets, wired at the composition root).
func New(resolver Resolver, chatter Chatter) ToolDecider {
	return &decider{resolver: resolver, chatter: chatter}
}

// SignalTool returns the single request_tool definition spliced into the
// writer's payload in router mode (ADR-0028 §2). It is NOT a registered tool:
// it has no handler and must not enter the Tool registry (which reserves the
// name).
func (d *decider) SignalTool() dto.ToolDef {
	return dto.ToolDef{
		Name:        "request_tool",
		Description: "Request an external action (retrieval, note access, editing, citation, vault search). Describe what you need in free text.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"intent":{"type":"string"}},"required":["intent"]}`),
	}
}

// Decide resolves one writer intent into a concrete tool call (interface.md
// §8b). It resolves needle-router via Fleet, renders the private router prompt,
// and calls Provider.Chat. A confident completion (FinishReason "tool", JSON
// content, confidence ≥ τ) yields a Decision with Name != ""; a refusal/empty
// completion yields the zero Decision (not an error); transport/protocol
// failures are returned as errors the loop labels router-unreachable.
func (d *decider) Decide(ctx context.Context, intent string, c dto.RouterContext) (dto.RouterResult, error) {
	res, err := d.resolver.Resolve(RouterModelName, dto.ResolveOpts{ModeTag: RouterModelName})
	if err != nil {
		return dto.RouterResult{}, err
	}
	target := dto.Target{BaseURL: res.Model.BaseURL, Capabilities: res.Model.Capabilities}

	prompt, breakdown := buildPrompt(intent, c)
	completion, err := d.chatter.Chat(ctx, target, dto.Request{
		ModelName: RouterModelName,
		Messages:  []dto.Message{{Role: "user", Content: prompt}},
		// The router facade owns the sampling contract (manifest defaults
		// temperature 0); a small token cap bounds the one-shot response.
		EffectiveParams: dto.SamplingParams{Temperature: 0, MaxTokens: 256},
	})
	if err != nil {
		return dto.RouterResult{}, err
	}

	decision, err := parseDecision(completion)
	if err != nil {
		return dto.RouterResult{}, err
	}

	return dto.RouterResult{
		Decision: decision,
		Usage: dto.RouterUsage{
			Breakdown: breakdown,
			Counts: dto.ProviderCounts{
				InputTokens:  completion.InputTokens,
				OutputTokens: completion.OutputTokens,
			},
		},
	}, nil
}

// buildPrompt renders the private router prompt and its per-component token
// breakdown (ADR-0028 §5: SystemPrompt=instruction, Tools=candidate schemas,
// Rag/History=arg context, User=intent, Thinking=0 always).
func buildPrompt(intent string, c dto.RouterContext) (string, dto.Breakdown) {
	var b strings.Builder

	b.WriteString(routerInstruction)
	b.WriteString("\n\n")

	toolsPart := ""
	if len(c.ToolDefs) > 0 {
		var sb strings.Builder
		for _, t := range c.ToolDefs {
			fmt.Fprintf(&sb, "- %s: %s\n  parameters: %s\n", t.Name, t.Description, string(t.Parameters))
		}
		toolsPart = "Candidate tools:\n" + sb.String()
		b.WriteString(toolsPart)
		b.WriteString("\n")
	}

	ragPart := ""
	if len(c.Chunks) > 0 {
		var sb strings.Builder
		for _, ch := range c.Chunks {
			fmt.Fprintf(&sb, "- [%s] %s\n", ch.Source, ch.Text)
		}
		ragPart = "Retrieved context:\n" + sb.String()
		b.WriteString(ragPart)
		b.WriteString("\n")
	}

	if c.Selection != nil {
		b.WriteString("The request targets block " + c.Selection.BlockID + ".\n")
	}

	historyPart := ""
	if len(c.History) > 0 {
		var sb strings.Builder
		for _, m := range c.History {
			fmt.Fprintf(&sb, "- %s: %s\n", m.Role, m.Content)
		}
		historyPart = "Recent conversation:\n" + sb.String()
		b.WriteString(historyPart)
		b.WriteString("\n")
	}

	userPart := "User request: " + intent
	b.WriteString(userPart)

	return b.String(), dto.Breakdown{
		SystemPrompt: estimate(routerInstruction),
		Tools:        estimate(toolsPart),
		Rag:          estimate(ragPart),
		History:      estimate(historyPart),
		User:         estimate(userPart),
	}
}

// parseDecision reads the router facade's completion into a Decision.
// FinishReason "tool" + JSON content = a candidate decision; τ is applied here,
// so a below-threshold confidence returns the zero Decision (refusal). An empty
// completion is a refusal. Non-empty content that is not valid JSON is a
// protocol violation — surfaced as an error, never silently degraded.
func parseDecision(c dto.Completion) (dto.Decision, error) {
	text := strings.TrimSpace(c.Text)
	if text == "" {
		return dto.Decision{}, nil // refusal / empty call (ADR-0028 §7)
	}
	var raw struct {
		Name       string          `json:"name"`
		Args       json.RawMessage `json:"args"`
		Confidence float32         `json:"confidence"`
	}
	if err := json.Unmarshal([]byte(text), &raw); err != nil {
		return dto.Decision{}, fmt.Errorf("router protocol violation: completion is not decision JSON: %w", err)
	}
	if raw.Name == "" || len(raw.Args) == 0 || raw.Confidence < defaultThreshold {
		return dto.Decision{}, nil // refused or below τ
	}
	return dto.Decision{Name: raw.Name, Args: raw.Args, Confidence: raw.Confidence}, nil
}

// estimate is the documented-unit token approximation (bytes/4), matching the
// assembler's unit (interface.md §5).
func estimate(s string) int {
	if s == "" {
		return 0
	}
	return (len(s) + 3) / 4
}
