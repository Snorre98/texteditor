// Package assembler holds the Context assembler — a pure, deterministic leaf
// (ADR-0016 §6, ADR-0011) that assembles the exact token payload for a call and
// returns a per-component token breakdown. It never calls the Retriever (chunks
// are handed in) and never reimplements a tokenizer for exact counts: the
// breakdown is a deterministic approximation in a documented unit, which the
// Token metering module later scales onto the provider's exact totals.
package assembler

import (
	"context"
	"fmt"

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
	system := in.Mode.SystemPrompt
	systemTokens := estimate(system)

	// Truncate history to the pipeline policy budget, dropping oldest first
	// (ADR-0015; one global policy since ADR-0045).
	history, historyDropped := truncateHistory(in.History, in.Policy.MaxHistoryTokens)

	// Truncate RAG chunks to the policy's RAG budget.
	rag, ragDropped := truncateRag(in.RAGChunks, in.Policy.MaxRagTokens)

	// Splice mentions after history, before user input (ADR-0036 §3). Truncate
	// over-budget mentions from the tail, with a labeled overflow line.
	mentions, mentionsDropped := truncateMentions(in.Mentions, in.Policy.MaxMentionTokens)

	// Build the assembled message list and the per-message provenance
	// (ADR-0044 §4). The two slices are positionally aligned: provenance[i]
	// describes messages[i].
	messages := []dto.Message{{Role: "system", Content: system}}
	provenance := []dto.MessageProvenance{{Role: "system", Component: "system", Tokens: systemTokens}}

	var historyTokens, ragTokens, mentionTokens int
	for _, m := range history {
		t := estimate(m.Content)
		messages = append(messages, m)
		provenance = append(provenance, dto.MessageProvenance{Role: m.Role, Component: "history", Tokens: t})
		historyTokens += t
	}
	for _, c := range rag {
		t := estimate(c.Text)
		messages = append(messages, dto.Message{Role: "user", Content: "Source: " + c.Text})
		provenance = append(provenance, dto.MessageProvenance{Role: "user", Component: "rag", Source: chunkSource(c), Tokens: t})
		ragTokens += t
	}
	for _, mc := range mentions {
		t := estimate(mc.Text)
		messages = append(messages, dto.Message{Role: "user", Content: mentionMarkup(mc)})
		provenance = append(provenance, dto.MessageProvenance{Role: "user", Component: "mention", Source: mc.Path, Tokens: t})
		mentionTokens += t
	}
	if mentionsDropped > 0 {
		// Labeled overflow line: the truncated mention tail is dropped, never
		// folded silently (failure-semantics §4). It is itself an assembled
		// message, so it carries provenance too.
		line := overflowLine()
		messages = append(messages, dto.Message{Role: "user", Content: line})
		provenance = append(provenance, dto.MessageProvenance{Role: "user", Component: "mention", Source: "<overflow>", Tokens: estimate(line)})
	}
	userTokens := estimate(in.UserInput)
	messages = append(messages, dto.Message{Role: "user", Content: in.UserInput})
	provenance = append(provenance, dto.MessageProvenance{Role: "user", Component: "user", Tokens: userTokens})

	// Tool schemas spliced as function definitions (their size is metered,
	// ADR-0011/0019).
	toolsTokens := 0
	for _, t := range in.Tools {
		toolsTokens += estimate(string(t.Parameters))
	}

	breakdown := dto.Breakdown{
		SystemPrompt: systemTokens,
		Tools:        toolsTokens,
		Rag:          ragTokens,
		History:      historyTokens,
		Mentions:     mentionTokens,
		User:         userTokens,
		Thinking:     0, // thinking is reconciled by the meter (ADR-0024)
	}

	// Labeled truncation records — history and RAG truncation were formerly
	// silent; every dropped item is now labeled (ADR-0044 §3, context-inspector
	// "Truncation is labeled, never silent").
	drops := []dto.ContextDrop{}
	if historyDropped > 0 {
		drops = append(drops, dto.ContextDrop{Component: "history", Reason: "history-budget", Count: historyDropped, Detail: budgetDetail("maxHistoryTokens", in.Policy.MaxHistoryTokens)})
	}
	if ragDropped > 0 {
		drops = append(drops, dto.ContextDrop{Component: "rag", Reason: "rag-budget", Count: ragDropped, Detail: budgetDetail("maxRagTokens", in.Policy.MaxRagTokens)})
	}
	if mentionsDropped > 0 {
		drops = append(drops, dto.ContextDrop{Component: "mention", Reason: "mention-budget", Count: mentionsDropped, Detail: budgetDetail("maxMentionTokens", in.Policy.MaxMentionTokens)})
	}

	// Per-component budget utilization (used vs the PipelinePolicy limit).
	// system/tools/user/thinking have no policy cap; their Limit stays 0.
	budget := []dto.BudgetUsage{
		{Component: "system", Used: systemTokens},
		{Component: "tools", Used: toolsTokens},
		{Component: "rag", Used: ragTokens, Limit: in.Policy.MaxRagTokens},
		{Component: "history", Used: historyTokens, Limit: in.Policy.MaxHistoryTokens},
		{Component: "mentions", Used: mentionTokens, Limit: in.Policy.MaxMentionTokens},
		{Component: "user", Used: userTokens},
		{Component: "thinking", Used: 0},
	}

	req := dto.Request{
		ModelName:       in.ModelName,
		Messages:        messages,
		Tools:           in.Tools,
		EffectiveParams: in.Params,
	}

	return dto.Payload{Messages: messages, Request: req, Provenance: provenance, Drops: drops, Budget: budget}, breakdown, nil
}

// truncateHistory returns the newest history messages that fit within maxTokens
// and the number of older messages dropped.
func truncateHistory(hist []dto.Message, maxTokens int) ([]dto.Message, int) {
	if maxTokens <= 0 || len(hist) == 0 {
		return nil, len(hist)
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
	return out, len(hist) - len(out)
}

// truncateRag returns the RAG chunks that fit within maxTokens and the number
// of trailing chunks dropped.
func truncateRag(chunks []dto.Chunk, maxTokens int) ([]dto.Chunk, int) {
	if maxTokens <= 0 {
		return nil, len(chunks)
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
	return out, len(chunks) - len(out)
}

// chunkSource is the provenance marker recorded for a retrieved chunk: the
// canonical path when present, else the chunk key, else the block id.
func chunkSource(c dto.Chunk) string {
	if c.Path != "" {
		return c.Path
	}
	if c.ChunkKey != "" {
		return c.ChunkKey
	}
	return c.BlockID
}

// budgetDetail renders a labeled drop's budget context.
func budgetDetail(name string, limit int) string {
	return fmt.Sprintf("%s=%d", name, limit)
}

// mentionMarkup wraps a mention in a path marker line so the model can cite it
// and clients can show provenance — the same "Source:" discipline as RAG source
// markers (ADR-0036 §3). Recorded marker format:
//
//	Source: <absolute path>\n<text>
//
// The path is verbatim (absolute, client-resolved per ADR-0036 §1).
func mentionMarkup(mc dto.MentionContent) string {
	return "Source: " + mc.Path + "\n" + mc.Text
}

// overflowLine is the labeled overflow line emitted when mentioned-file content
// exceeds the policy's MaxMentionTokens and is truncated from the tail
// (failure-semantics §4: overflow is labeled, never folded silently).
func overflowLine() string {
	return "Source: <overflow>: some mentioned-file content was truncated to fit the mention token budget"
}

// truncateMentions keeps mentions that fit within maxTokens, truncating
// over-budget mentions from the tail (last mention first, ADR-0036 §4). A
// maxTokens of 0 truncates all mention content. It returns the kept mentions and
// the number dropped; the assembler renders a labeled overflow line so
// truncation is never silent (failure-semantics §4).
func truncateMentions(mentions []dto.MentionContent, maxTokens int) ([]dto.MentionContent, int) {
	if maxTokens <= 0 {
		return nil, len(mentions)
	}
	out := make([]dto.MentionContent, 0, len(mentions))
	total := 0
	for i, m := range mentions {
		t := estimate(m.Text)
		if total+t > maxTokens {
			return out, len(mentions) - i
		}
		out = append(out, m)
		total += t
	}
	return out, 0
}

// estimate is the documented-unit token approximation (bytes/4).
func estimate(s string) int {
	if s == "" {
		return 0
	}
	return (len(s) + 3) / 4
}
