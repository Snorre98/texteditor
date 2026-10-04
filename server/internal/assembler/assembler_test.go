package assembler

import (
	"context"
	"encoding/json"
	"reflect"
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
		},
		ModelName: "gemma4-12b",
		Params:    dto.SamplingParams{Temperature: 0.3, MaxTokens: 100},
		Policy: dto.PipelinePolicy{
			MaxHistoryTokens: 1000,
			MaxRagTokens:     1000,
			MaxMentionTokens: 1000,
		},
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
		},
		Policy: dto.PipelinePolicy{
			MaxHistoryTokens: 4, // ~16 bytes → roughly one short message
			MaxRagTokens:     4,
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
		},
		Policy: dto.PipelinePolicy{
			MaxMentionTokens: 1000,
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
		},
		Policy: dto.PipelinePolicy{
			MaxMentionTokens: 6, // small budget → truncates the tail
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
		},
		Policy: dto.PipelinePolicy{
			MaxMentionTokens: 0, // no mention budget → all content truncated
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
	kept, dropped := truncateMentions(mentions, 2) // ~8 bytes → only "aaaa" fits
	if dropped != 2 {
		t.Fatalf("dropped = %d, want 2", dropped)
	}
	if len(kept) != 1 || kept[0].Path != "/a" {
		t.Fatalf("kept = %+v, want [ /a ] (tail-first truncation)", kept)
	}

	kept, dropped = truncateMentions(mentions, 1000)
	if dropped != 0 || len(kept) != 3 {
		t.Fatalf("kept = %+v dropped=%d, want all 3, none dropped", kept, dropped)
	}

	kept, dropped = truncateMentions(mentions, 0)
	if dropped != 3 || len(kept) != 0 {
		t.Fatalf("zero budget: kept=%+v dropped=%d, want empty + 3 dropped", kept, dropped)
	}
}

// TestProvenancePerMessage covers context-inspector "A completed turn persists a
// context snapshot": every assembled message carries its component and source.
func TestProvenancePerMessage(t *testing.T) {
	a := New()
	in := dto.AssemblerInput{
		Mode: dto.Mode{SystemPrompt: "sys"},
		Policy: dto.PipelinePolicy{
			MaxHistoryTokens: 1000, MaxRagTokens: 1000, MaxMentionTokens: 1000,
		},
		History: []dto.Message{
			{Role: "user", Content: "earlier"},
			{Role: "assistant", Content: "reply"},
		},
		RAGChunks: []dto.Chunk{{BlockID: "b1", Text: "passage", Path: "/vault/a.md"}},
		Mentions:  []dto.MentionContent{{Path: "/vault/b.md", Text: "note"}},
		UserInput: "fix this",
	}
	p, _, err := a.Assemble(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Provenance) != len(p.Messages) {
		t.Fatalf("provenance %d != messages %d", len(p.Provenance), len(p.Messages))
	}
	wantComponents := []string{"system", "history", "history", "rag", "mention", "user"}
	got := make([]string, 0, len(p.Provenance))
	for i, prov := range p.Provenance {
		got = append(got, prov.Component)
		if prov.Role != p.Messages[i].Role {
			t.Fatalf("provenance[%d].Role = %q, message role = %q", i, prov.Role, p.Messages[i].Role)
		}
	}
	if !reflect.DeepEqual(got, wantComponents) {
		t.Fatalf("components = %v, want %v", got, wantComponents)
	}
	if p.Provenance[3].Source != "/vault/a.md" {
		t.Fatalf("rag provenance source = %q, want the chunk path", p.Provenance[3].Source)
	}
	if p.Provenance[4].Source != "/vault/b.md" {
		t.Fatalf("mention provenance source = %q, want the mention path", p.Provenance[4].Source)
	}
	for _, prov := range p.Provenance {
		if prov.Pinned {
			t.Fatalf("no C3 item is pinned: %+v", prov)
		}
	}
}

// TestHistoryAndRagTruncationDropsLabeled covers context-inspector "Truncation
// is labeled, never silent": silent history/RAG truncation now records a drop
// with a count, and the breakdown still sums.
func TestHistoryAndRagTruncationDropsLabeled(t *testing.T) {
	a := New()
	p, b, err := a.Assemble(context.Background(), dto.AssemblerInput{
		Mode:   dto.Mode{SystemPrompt: "sys"},
		Policy: dto.PipelinePolicy{MaxHistoryTokens: 4, MaxRagTokens: 4, MaxMentionTokens: 1000},
		History: []dto.Message{
			{Role: "user", Content: "aaaaaaaaaaaaaaaaaaaa"}, // oldest, oversized
			{Role: "assistant", Content: "ok"},
		},
		RAGChunks: []dto.Chunk{
			{BlockID: "b1", Text: "tiny"},
			{BlockID: "b2", Text: "xxxxxxxxxxxxxxxxxxxxxxxxxxxx"},
		},
		UserInput: "hi",
	})
	if err != nil {
		t.Fatal(err)
	}

	drops := map[string]dto.ContextDrop{}
	for _, d := range p.Drops {
		drops[d.Component] = d
	}
	if d, ok := drops["history"]; !ok || d.Count != 1 {
		t.Fatalf("history drop = %+v, want count 1", d)
	}
	if d, ok := drops["rag"]; !ok || d.Count != 1 {
		t.Fatalf("rag drop = %+v, want count 1", d)
	}
	if b.History > 4 || b.Rag > 4 {
		t.Fatalf("breakdown not truncated: %+v", b)
	}
}

// TestMentionOverflowRecordsDrop: the labeled overflow line is accompanied by a
// mention drop record.
func TestMentionOverflowRecordsDrop(t *testing.T) {
	a := New()
	p, _, err := a.Assemble(context.Background(), dto.AssemblerInput{
		Mode:      dto.Mode{SystemPrompt: "sys"},
		Policy:    dto.PipelinePolicy{MaxMentionTokens: 6},
		UserInput: "hi",
		Mentions: []dto.MentionContent{
			{Path: "/a.md", Text: "aaaa"},
			{Path: "/b.md", Text: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var mentionDrop *dto.ContextDrop
	for i := range p.Drops {
		if p.Drops[i].Component == "mention" {
			mentionDrop = &p.Drops[i]
		}
	}
	if mentionDrop == nil || mentionDrop.Count != 1 {
		t.Fatalf("mention drop = %+v, want count 1", mentionDrop)
	}
}

// TestBudgetUtilization: every component is accounted with its policy limit.
func TestBudgetUtilization(t *testing.T) {
	a := New()
	p, b, err := a.Assemble(context.Background(), dto.AssemblerInput{
		Mode:      dto.Mode{SystemPrompt: "sys"},
		Policy:    dto.PipelinePolicy{MaxHistoryTokens: 100, MaxRagTokens: 200, MaxMentionTokens: 300},
		History:   []dto.Message{{Role: "user", Content: "hello history"}},
		RAGChunks: []dto.Chunk{{BlockID: "b1", Text: "a rag passage"}},
		Mentions:  []dto.MentionContent{{Path: "/a.md", Text: "a mention"}},
		UserInput: "input",
	})
	if err != nil {
		t.Fatal(err)
	}
	usage := map[string]dto.BudgetUsage{}
	for _, u := range p.Budget {
		usage[u.Component] = u
	}
	for _, comp := range []string{"system", "tools", "rag", "history", "mentions", "user", "thinking"} {
		if _, ok := usage[comp]; !ok {
			t.Fatalf("budget missing component %q: %+v", comp, p.Budget)
		}
	}
	// The three budgeted components carry their policy limit; used matches the
	// deterministic breakdown (the numbers the meter scales).
	if u := usage["history"]; u.Limit != 100 || u.Used != b.History {
		t.Fatalf("history budget = %+v, want limit 100 used %d", u, b.History)
	}
	if u := usage["rag"]; u.Limit != 200 || u.Used != b.Rag {
		t.Fatalf("rag budget = %+v, want limit 200 used %d", u, b.Rag)
	}
	if u := usage["mentions"]; u.Limit != 300 || u.Used != b.Mentions {
		t.Fatalf("mentions budget = %+v, want limit 300 used %d", u, b.Mentions)
	}
	// Uncapped components carry no limit.
	for _, comp := range []string{"system", "tools", "user", "thinking"} {
		if usage[comp].Limit != 0 {
			t.Fatalf("component %q should have no limit: %+v", comp, usage[comp])
		}
	}
}

// TestAssemblePayloadPurity: same inputs → identical payload, including the new
// provenance/drops/budget slices (R4, ADR-0016 §6).
func TestAssemblePayloadPurity(t *testing.T) {
	a := New()
	in := dto.AssemblerInput{
		Mode:      dto.Mode{SystemPrompt: "sys"},
		Policy:    dto.PipelinePolicy{MaxHistoryTokens: 1000, MaxRagTokens: 1000, MaxMentionTokens: 1000},
		History:   []dto.Message{{Role: "user", Content: "earlier"}},
		RAGChunks: []dto.Chunk{{BlockID: "b1", Text: "passage", Path: "/vault/a.md"}},
		Mentions:  []dto.MentionContent{{Path: "/vault/b.md", Text: "note"}},
		UserInput: "fix this",
	}
	p1, b1, err := a.Assemble(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	p2, b2, err := a.Assemble(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p1, p2) {
		t.Fatalf("payload not deterministic:\n%+v\n%+v", p1, p2)
	}
	if b1 != b2 {
		t.Fatalf("breakdown not deterministic")
	}
}
