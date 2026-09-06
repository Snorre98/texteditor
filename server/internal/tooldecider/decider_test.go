package tooldecider

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"texteditor/shared/dto"
)

// --------------------------- minimal stubs ---------------------------

type stubResolver struct {
	res dto.Resolution
	err error
	got struct {
		name    string
		modeTag string
	}
}

func (s *stubResolver) Resolve(name string, opts dto.ResolveOpts) (dto.Resolution, error) {
	s.got.name = name
	s.got.modeTag = opts.ModeTag
	return s.res, s.err
}

type stubChatter struct {
	completion dto.Completion
	err        error
	got        dto.Request
}

func (s *stubChatter) Chat(_ context.Context, _ dto.Target, req dto.Request) (dto.Completion, error) {
	s.got = req
	return s.completion, s.err
}

func routerCtx() dto.RouterContext {
	return dto.RouterContext{
		ToolDefs: []dto.ToolDef{
			{Name: "edit_markdown", Description: "edit a block", Parameters: json.RawMessage(`{"type":"object"}`)},
			{Name: "retrieve", Description: "retrieve chunks", Parameters: json.RawMessage(`{"type":"object"}`)},
		},
		Chunks:    []dto.Chunk{{BlockID: "b1", Text: "chunk text", Source: "b1"}},
		Selection: &dto.Selection{BlockID: "b9"},
		History:   []dto.Message{{Role: "user", Content: "earlier"}},
		UserInput: "fix this",
	}
}

// --------------------------- tests ---------------------------

func TestSignalToolShape(t *testing.T) {
	d := New(&stubResolver{}, &stubChatter{})
	tool := d.SignalTool()
	if tool.Name != "request_tool" {
		t.Fatalf("name = %q, want request_tool", tool.Name)
	}
	if tool.Description == "" {
		t.Fatal("empty description")
	}
	var params struct {
		Type       string `json:"type"`
		Properties struct {
			Intent struct {
				Type string `json:"type"`
			} `json:"intent"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(tool.Parameters, &params); err != nil {
		t.Fatalf("parameters not JSON: %v", err)
	}
	if params.Type != "object" || params.Properties.Intent.Type != "string" ||
		len(params.Required) != 1 || params.Required[0] != "intent" {
		t.Fatalf("parameters = %+v, want the ADR-0028 §2 request_tool schema", params)
	}
}

func TestDecideConfident(t *testing.T) {
	resolver := &stubResolver{res: dto.Resolution{
		Model: dto.Model{Name: "needle-router", BaseURL: "http://127.0.0.1:8081/v1"},
	}}
	chatter := &stubChatter{completion: dto.Completion{
		Text:         `{"name":"edit_markdown","args":{"blockId":"b9"},"confidence":0.9}`,
		FinishReason: "tool",
		InputTokens:  120,
		OutputTokens: 20,
	}}
	d := New(resolver, chatter)

	res, err := d.Decide(context.Background(), "rewrite this block", routerCtx())
	if err != nil {
		t.Fatal(err)
	}
	if res.Decision.Name != "edit_markdown" {
		t.Fatalf("decision = %+v, want edit_markdown", res.Decision)
	}
	if res.Decision.Confidence != 0.9 {
		t.Fatalf("confidence = %v, want 0.9", res.Decision.Confidence)
	}
	var args map[string]interface{}
	if err := json.Unmarshal(res.Decision.Args, &args); err != nil || args["blockId"] != "b9" {
		t.Fatalf("args = %s, want blockId b9", res.Decision.Args)
	}

	// Resolution is by name, needle-router, tagged with itself (ADR-0028 §7).
	if resolver.got.name != RouterModelName || resolver.got.modeTag != RouterModelName {
		t.Fatalf("resolved %q (tag %q), want needle-router", resolver.got.name, resolver.got.modeTag)
	}
	// The provider call is a single-user-message, tool-less request.
	if chatter.got.ModelName != RouterModelName {
		t.Fatalf("model = %q", chatter.got.ModelName)
	}
	if len(chatter.got.Messages) != 1 || chatter.got.Messages[0].Role != "user" {
		t.Fatalf("messages = %+v, want one user message", chatter.got.Messages)
	}
	if len(chatter.got.Tools) != 0 {
		t.Fatalf("tools = %+v, want none (candidate schemas ride the prompt)", chatter.got.Tools)
	}

	// Metering inputs (ADR-0028 §5): every component populated, Thinking == 0.
	if res.Usage.Counts.InputTokens != 120 || res.Usage.Counts.OutputTokens != 20 {
		t.Fatalf("usage counts = %+v", res.Usage.Counts)
	}
	b := res.Usage.Breakdown
	if b.SystemPrompt == 0 || b.Tools == 0 || b.Rag == 0 || b.History == 0 || b.User == 0 {
		t.Fatalf("breakdown = %+v, want all components populated", b)
	}
	if b.Thinking != 0 {
		t.Fatalf("thinking = %d, want 0 always (ADR-0028 §5)", b.Thinking)
	}
}

func TestDecideRefusalEmpty(t *testing.T) {
	chatter := &stubChatter{completion: dto.Completion{FinishReason: "stop"}}
	d := New(&stubResolver{res: dto.Resolution{Model: dto.Model{Name: "needle-router"}}}, chatter)

	res, err := d.Decide(context.Background(), "hello", routerCtx())
	if err != nil {
		t.Fatal(err)
	}
	if res.Decision.Name != "" {
		t.Fatalf("decision = %+v, want empty (refusal)", res.Decision)
	}
	// Refusal is still a metered router call (ADR-0028 §5).
	if res.Usage.Breakdown.User == 0 {
		t.Fatalf("refusal breakdown missing user component: %+v", res.Usage.Breakdown)
	}
}

func TestDecideBelowThreshold(t *testing.T) {
	chatter := &stubChatter{completion: dto.Completion{
		Text:         `{"name":"retrieve","args":{"query":"x"},"confidence":0.4}`,
		FinishReason: "tool",
	}}
	d := New(&stubResolver{res: dto.Resolution{Model: dto.Model{Name: "needle-router"}}}, chatter)

	res, err := d.Decide(context.Background(), "find", routerCtx())
	if err != nil {
		t.Fatal(err)
	}
	if res.Decision.Name != "" {
		t.Fatalf("decision = %+v, want empty (confidence 0.4 < τ)", res.Decision)
	}
}

func TestDecideTransportErrors(t *testing.T) {
	t.Run("resolve error", func(t *testing.T) {
		boom := errors.New("daemon-unreachable")
		d := New(&stubResolver{err: boom}, &stubChatter{})
		if _, err := d.Decide(context.Background(), "x", routerCtx()); !errors.Is(err, boom) {
			t.Fatalf("want resolver error propagated, got %v", err)
		}
	})
	t.Run("chatter error", func(t *testing.T) {
		boom := errors.New("provider-unreachable")
		d := New(&stubResolver{res: dto.Resolution{Model: dto.Model{Name: "needle-router"}}}, &stubChatter{err: boom})
		if _, err := d.Decide(context.Background(), "x", routerCtx()); !errors.Is(err, boom) {
			t.Fatalf("want chatter error propagated, got %v", err)
		}
	})
}

func TestDecideProtocolViolation(t *testing.T) {
	chatter := &stubChatter{completion: dto.Completion{Text: "just prose", FinishReason: "stop"}}
	d := New(&stubResolver{res: dto.Resolution{Model: dto.Model{Name: "needle-router"}}}, chatter)
	if _, err := d.Decide(context.Background(), "x", routerCtx()); err == nil {
		t.Fatal("want a labeled protocol error for non-JSON router content, got nil")
	}
}
