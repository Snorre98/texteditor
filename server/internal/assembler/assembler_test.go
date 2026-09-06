package assembler

import (
	"context"
	"encoding/json"
	"strings"
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
		ModelName: "gemma4-12b",
		Params:    dto.SamplingParams{Temperature: 0.3, MaxTokens: 100},
		Tools: []dto.ToolDef{
			{Name: "edit_markdown", Description: "edits a block", Parameters: json.RawMessage(`{"type":"object"}`)},
		},
		History:   []dto.Message{{Role: "user", Content: "earlier"}, {Role: "assistant", Content: "reply"}},
		RAGChunks: []dto.Chunk{{BlockID: "b1", Text: "a source passage here"}},
		UserInput: "fix this sentence",
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
	if p1.Request.ModelName != p2.Request.ModelName || len(p1.Request.Messages) != len(p2.Request.Messages) {
		t.Fatalf("request not deterministic")
	}
	if p1.Request.ModelName != "gemma4-12b" {
		t.Fatalf("request model = %q", p1.Request.ModelName)
	}
	if len(p1.Request.Tools) != 1 || p1.Request.Tools[0].Name != "edit_markdown" {
		t.Fatalf("request tools = %+v", p1.Request.Tools)
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
			{Role: "assistant", Content: "ok"},              // newest
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

func TestAssembleMentionsSpliced(t *testing.T) {
	a := New()
	in := dto.AssemblerInput{
		Mode: dto.Mode{
			SystemPrompt: "sys",
			ContextBudget: dto.ContextBudget{
				MaxMentionTokens: 1000,
			},
		},
		UserInput: "summarize",
		Mentions: []dto.MentionContent{
			{Path: "/a/one.md", Text: "first mention"},
			{Path: "/b/two.md", Text: "second mention"},
		},
	}
	p, b, err := a.Assemble(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if b.Mentions <= 0 {
		t.Fatalf("mentions breakdown should be positive: %+v", b)
	}

	// Order: system, mention(one), mention(two), user. Mentions sit after
	// history and before the user input.
	if len(p.Messages) != 4 {
		t.Fatalf("messages = %d, want 4: %+v", len(p.Messages), p.Messages)
	}
	if p.Messages[0].Role != "system" {
		t.Fatalf("first should be system: %+v", p.Messages[0])
	}
	if p.Messages[1].Role != "user" || p.Messages[1].Content != "Source: /a/one.md\nfirst mention" {
		t.Fatalf("mention 1 = %+v", p.Messages[1])
	}
	if p.Messages[2].Role != "user" || p.Messages[2].Content != "Source: /b/two.md\nsecond mention" {
		t.Fatalf("mention 2 = %+v", p.Messages[2])
	}
	if p.Messages[3].Content != "summarize" {
		t.Fatalf("user input = %q, want summarize", p.Messages[3].Content)
	}
}

func TestAssembleMentionsTruncateTailAndOverflow(t *testing.T) {
	a := New()
	in := dto.AssemblerInput{
		Mode: dto.Mode{
			SystemPrompt: "sys",
			ContextBudget: dto.ContextBudget{
				MaxMentionTokens: 6, // small budget → truncates the tail
			},
		},
		UserInput: "hi",
		Mentions: []dto.MentionContent{
			{Path: "/a.md", Text: "aaaa"},                             // 1 token
			{Path: "/b.md", Text: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}, // large, truncated from the tail
		},
	}
	p, b, err := a.Assemble(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if b.Mentions == 0 {
		t.Fatalf("mentions should keep the first: %+v", b)
	}
	if b.Mentions > 6 {
		t.Fatalf("mentions not truncated: %+v", b)
	}

	// The oversized tail must not appear; an overflow line must.
	var sawOverflow bool
	for _, m := range p.Messages {
		if strings.Contains(m.Content, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb") {
			t.Fatal("truncated mention tail survived")
		}
		if strings.Contains(m.Content, "<overflow>") {
			sawOverflow = true
		}
	}
	if !sawOverflow {
		t.Fatalf("no labeled overflow line: %+v", p.Messages)
	}
}

func TestAssembleMentionsZeroBudget(t *testing.T) {
	a := New()
	in := dto.AssemblerInput{
		Mode: dto.Mode{
			SystemPrompt: "sys",
			ContextBudget: dto.ContextBudget{
				MaxMentionTokens: 0, // no mention budget → all content truncated
			},
		},
		UserInput: "hi",
		Mentions: []dto.MentionContent{
			{Path: "/a.md", Text: "content"},
		},
	}
	p, b, err := a.Assemble(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if b.Mentions != 0 {
		t.Fatalf("mentions should be 0 with zero budget: %+v", b)
	}
	var sawContent, sawOverflow bool
	for _, m := range p.Messages {
		if strings.Contains(m.Content, "Source: /a.md") {
			sawContent = true
		}
		if strings.Contains(m.Content, "<overflow>") {
			sawOverflow = true
		}
	}
	if sawContent {
		t.Fatalf("mention content leaked with zero budget: %+v", p.Messages)
	}
	if !sawOverflow {
		t.Fatalf("zero budget must still label the overflow: %+v", p.Messages)
	}
}

func TestTruncateMentionsPure(t *testing.T) {
	mc := func(p, txt string) dto.MentionContent { return dto.MentionContent{Path: p, Text: txt} }
	mentions := []dto.MentionContent{
		mc("/a", "aaaa"),
		mc("/b", "bbbbbbbb"),
		mc("/c", "cccc"),
	}
	kept, overflow := truncateMentions(mentions, 2) // ~8 bytes → only "aaaa" fits
	if !overflow {
		t.Fatal("expected overflow")
	}
	if len(kept) != 1 || kept[0].Path != "/a" {
		t.Fatalf("kept = %+v, want [ /a ] (tail-first truncation)", kept)
	}

	kept, overflow = truncateMentions(mentions, 1000)
	if overflow || len(kept) != 3 {
		t.Fatalf("kept = %+v overflow=%v, want all 3, no overflow", kept, overflow)
	}

	kept, overflow = truncateMentions(mentions, 0)
	if !overflow || len(kept) != 0 {
		t.Fatalf("zero budget: kept=%+v overflow=%v, want empty + overflow", kept, overflow)
	}
}
