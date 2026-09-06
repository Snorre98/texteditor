// Package assembler holds the Context assembler — a pure, deterministic leaf
// (ADR-0016 §6, ADR-0011) that assembles the exact token payload for a call and
// returns a per-component token breakdown. It never calls the Retriever (chunks
// are handed in) and never reimplements a tokenizer for exact counts: the
// breakdown is a deterministic approximation in a documented unit, which the
// Token metering module later scales onto the provider's exact totals.
package assembler

import (
	"context"

	"texteditor/shared/dto"
)

// ContextAssembler is the Context assembler public API (interface.md §5).
type ContextAssembler interface {
	Assemble(ctx context.Context, in dto.AssemblerInput) (dto.Payload, dto.Breakdown, error)
}

// Interface is an alias for ContextAssembler (the contracted name, interface.md §5).
type Interface = ContextAssembler

// assembler is the concrete Context assembler (pure leaf).
type assembler struct{}

// New returns the default Context assembler.
func New() ContextAssembler { return assembler{} }

// Assemble builds the provider-ready payload and its deterministic per-component
// breakdown. Same inputs → same payload/breakdown (R4, ADR-0016 §6).
//
// Documented unit: token estimates are bytes/4 — a deterministic approximation
// for budgeting; authoritative counts are the provider's prompt_eval_count/
// eval_count, applied by the meter.
func (assembler) Assemble(_ context.Context, in dto.AssemblerInput) (dto.Payload, dto.Breakdown, error) {
	var systemBuilder []string
	systemTokenParts := 0
	if in.Mode.Preamble != "" {
		systemBuilder = append(systemBuilder, in.Mode.Preamble)
		systemTokenParts += estimate(in.Mode.Preamble)
	}
	systemBuilder = append(systemBuilder, in.Mode.SystemPrompt)
	systemTokenParts += estimate(in.Mode.SystemPrompt)
	system := joinPara(systemBuilder)

	// Truncate history to the mode's budget, dropping oldest first (ADR-0015).
	history := truncateHistory(in.History, in.Mode.ContextBudget.MaxHistoryTokens)

	// Truncate RAG chunks to the mode's RAG budget.
	rag := truncateRag(in.RAGChunks, in.Mode.ContextBudget.MaxRagTokens)

	// Build the assembled message list.
	messages := []dto.Message{{Role: "system", Content: system}}
	var historyTokens, ragTokens int
	for _, m := range history {
		messages = append(messages, m)
		historyTokens += estimate(m.Content)
	}
	for _, c := range rag {
		messages = append(messages, dto.Message{Role: "user", Content: "Source: " + c.Text})
		ragTokens += estimate(c.Text)
	}
	messages = append(messages, dto.Message{Role: "user", Content: in.UserInput})

	// Tool schemas spliced as function definitions (their size is metered,
	// ADR-0011/0019).
	toolsTokens := 0
	for _, t := range in.Tools {
		toolsTokens += estimate(string(t.Parameters))
	}

	breakdown := dto.Breakdown{
		SystemPrompt: systemTokenParts,
		Tools:        toolsTokens,
		Rag:          ragTokens,
		History:      historyTokens,
		User:         estimate(in.UserInput),
		Thinking:     0, // thinking is reconciled by the meter (ADR-0024)
	}

	req := dto.Request{
		ModelName:       in.ModelName,
		Messages:        messages,
		Tools:           in.Tools,
		EffectiveParams: effectiveParams(in.Mode, in.Params),
	}

	return dto.Payload{Messages: messages, Request: req}, breakdown, nil
}

// effectiveParams reflects the merged sampling params the Provider must render.
// The Fleet gateway already merged manifest defaults ← mode.params ← overrides
// into the Resolution; the caller passes that merged result via in.Params. When
// empty (e.g. a stub test path), fall back to the mode's params so the request is
// never parameter-less.
func effectiveParams(m dto.Mode, merged dto.SamplingParams) dto.SamplingParams {
	if merged.Temperature != 0 || merged.MaxTokens != 0 {
		return merged
	}
	return m.Params
}

// truncateHistory returns the newest history messages that fit within maxTokens.
func truncateHistory(hist []dto.Message, maxTokens int) []dto.Message {
	if maxTokens <= 0 || len(hist) == 0 {
		return nil
	}
	var out []dto.Message
	total := 0
	for i := len(hist) - 1; i >= 0; i-- {
		t := estimate(hist[i].Content)
		if total+t > maxTokens && len(out) > 0 {
			break
		}
		out = append(out, hist[i])
		total += t
	}
	// Reverse back to chronological order.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// truncateRag returns the newest RAG chunks that fit within maxTokens.
func truncateRag(chunks []dto.Chunk, maxTokens int) []dto.Chunk {
	if maxTokens <= 0 {
		return nil
	}
	var out []dto.Chunk
	total := 0
	for _, c := range chunks {
		t := estimate(c.Text)
		if total+t > maxTokens && len(out) > 0 {
			break
		}
		out = append(out, c)
		total += t
	}
	return out
}

// estimate is the documented-unit token approximation (bytes/4).
func estimate(s string) int {
	if s == "" {
		return 0
	}
	return (len(s) + 3) / 4
}

func joinPara(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += "\n\n"
		}
		out += p
	}
	return out
}
