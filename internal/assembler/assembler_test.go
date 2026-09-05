package assembler

import (
	"context"
	"encoding/json"
	"testing"

	"texteditor/shared/dto"
)

func TestAssembleDeterministic(t *testing.T) {
	a := New()
	in := dto.AssemblerInput{
		Mode: dto.Mode{
			Name:         "proofreader",
			SystemPrompt: "You are a proofreader.",
			Params:       dto.SamplingParams{Temperature: 0.3, MaxTokens: 100},
			ContextBudget: dto.ContextBudget{
				MaxHistoryTokens: 1000,
				MaxRagTokens:     1000,
			},
			ToolAllowlist: []string{"edit_markdown"},
		},
		ToolSchemas: []dto.JSONSchema{json.RawMessage(`{"type":"object"}`)},
		History:     []dto.Message{{Role: "user", Content: "earlier"}, {Role: "assistant", Content: "reply"}},
		RAGChunks:   []dto.Chunk{{BlockID: "b1", Text: "a source passage here"}},
		UserInput:   "fix this sentence",
	}

	p1, b1, err := a.Assemble(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	p2, b2, err := a.Assemble(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}

	if b1 != b2 {
		t.Fatalf("breakdown not deterministic: %+v vs %+v", b1, b2)
	}
	if string(p1.Request) != string(p2.Request) {
		t.Fatalf("payload not deterministic:\n%s\n%s", p1.Request, p2.Request)
	}

	// Breakdown components sum positively where present.
	if b1.SystemPrompt <= 0 || b1.History <= 0 || b1.Rag <= 0 || b1.User <= 0 || b1.Tools <= 0 {
		t.Fatalf("breakdown components should be positive: %+v", b1)
	}
	if b1.Thinking != 0 {
		t.Fatalf("thinking should be 0 in the assembler (meter reconciles): %d", b1.Thinking)
	}

	// Messages order: system, history(2), rag(1), user(1).
	if len(p1.Messages) != 5 {
		t.Fatalf("messages = %d, want 5", len(p1.Messages))
	}
	if p1.Messages[0].Role != "system" {
		t.Fatalf("first message role = %s", p1.Messages[0].Role)
	}
	if p1.Messages[4].Role != "user" || p1.Messages[4].Content != "fix this sentence" {
		t.Fatalf("last message = %+v", p1.Messages[4])
	}
}

func TestAssembleTruncatesHistoryAndRag(t *testing.T) {
	a := New()
	big := dto.AssemblerInput{
		Mode: dto.Mode{
			SystemPrompt: "sys",
			ContextBudget: dto.ContextBudget{
				MaxHistoryTokens: 4, // ~16 bytes → roughly one short message
				MaxRagTokens:     4,
			},
		},
		History: []dto.Message{
			{Role: "user", Content: "aaaaaaaaaaaaaaaaaaaa"}, // oldest, large
			{Role: "assistant", Content: "ok"},               // newest
		},
		RAGChunks: []dto.Chunk{
			{BlockID: "b1", Text: "tiny"},
			{BlockID: "b2", Text: "xxxxxxxxxxxxxxxxxxxxxxxxxxxx"},
		},
		UserInput: "hi",
	}
	p, b, err := a.Assemble(context.Background(), big)
	if err != nil {
		t.Fatal(err)
	}
	// History truncated: keeps newest ("ok"), drops the oversized oldest.
	if b.History > 4 {
		t.Fatalf("history not truncated: %d tokens", b.History)
	}
	// RAG truncated to budget: only the small first chunk fits.
	if b.Rag > 4 {
		t.Fatalf("rag not truncated: %d tokens", b.Rag)
	}
	if b.Rag == 0 {
		t.Fatal("rag should keep at least the first chunk")
	}
	// The aged-out oversized chunks must not appear.
	for _, m := range p.Messages {
		if m.Content == "aaaaaaaaaaaaaaaaaaaa" || m.Content == "Source: xxxxxxxxxxxxxxxxxxxxxxxxxxxx" {
			t.Fatal("oversized chunk survived truncation")
		}
	}
}

func TestAssembleEmptyHistoryRag(t *testing.T) {
	a := New()
	in := dto.AssemblerInput{
		Mode:      dto.Mode{SystemPrompt: "sys"},
		UserInput: "hello",
	}
	_, b, err := a.Assemble(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if b.History != 0 || b.Rag != 0 || b.Tools != 0 {
		t.Fatalf("empty components should be 0: %+v", b)
	}
}
