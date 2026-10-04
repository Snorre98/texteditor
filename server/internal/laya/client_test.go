package laya

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"texteditor/shared/dto"
)

// fakeResolver resolves the named model to a fixed base URL (the Fleet
// projection ends in /v1, matching the real gateway).
type fakeResolver struct {
	base string
	err  error
}

func (f fakeResolver) Resolve(name string, opts dto.ResolveOpts) (dto.Resolution, error) {
	if f.err != nil {
		return dto.Resolution{}, f.err
	}
	return dto.Resolution{Model: dto.Model{Name: name, BaseURL: f.base}}, nil
}

func newServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, Interface) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv, New(fakeResolver{base: srv.URL + "/v1"}, "laya", time.Second)
}

func TestPlanParsesTypedAnswers(t *testing.T) {
	_, c := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/systemone" {
			t.Errorf("path = %s, want /v1/systemone", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"model":"laya","answers":{
			"retrieve":{"type":"noul","noul":0.81,"answer_confidence":0.9},
			"thinking":{"type":"choice","choice":"on","answer_confidence":0.9},
			"breadth":{"type":"choice","choice":"many","answer_confidence":0.9}},
			"usage":{"input_tokens":120,"output_tokens":3},"routing":{"model":"english"}}`))
	})
	plan := c.Plan(context.Background(), dto.DecisionPlannerInput{Request: "expand the methods"})
	if plan.Degraded {
		t.Fatalf("unexpected degrade: %s", plan.Reason)
	}
	if !plan.Retrieve || plan.Thinking != dto.ThinkingOn || plan.Breadth != dto.DecisionBreadthMany {
		t.Fatalf("plan = %+v", plan)
	}
	if plan.Checkpoint != "english" || plan.PromptTokens != 120 || plan.CompletionTokens != 3 {
		t.Fatalf("plan meta = %+v", plan)
	}
}

func TestPlanDegradesWhenUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	base := srv.URL + "/v1"
	srv.Close() // connection refused
	c := New(fakeResolver{base: base}, "laya", time.Second)
	plan := c.Plan(context.Background(), dto.DecisionPlannerInput{Request: "x"})
	if !plan.Degraded || plan.Reason != dto.DecisionDegradedUnreachable {
		t.Fatalf("plan = %+v, want degraded unreachable", plan)
	}
}

func TestPlanDegradesOnProtocol(t *testing.T) {
	_, c := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"answers":{"retrieve":{"noul":0.9}}}`)) // missing thinking/breadth
	})
	plan := c.Plan(context.Background(), dto.DecisionPlannerInput{Request: "x"})
	if !plan.Degraded || plan.Reason != dto.DecisionDegradedProtocol {
		t.Fatalf("plan = %+v, want degraded protocol", plan)
	}
}

func TestGateAppliesThresholdAndKeepsLowConfidence(t *testing.T) {
	_, c := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/systemone/batch" {
			t.Errorf("path = %s, want /v1/systemone/batch", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"results":[
			{"model":"laya","answers":{"relevant":{"type":"noul","noul":0.9,"answer_confidence":0.9}},"usage":{"input_tokens":40,"output_tokens":1},"routing":{"model":"english"}},
			{"model":"laya","answers":{"relevant":{"type":"noul","noul":0.1,"answer_confidence":0.9}},"usage":{"input_tokens":40,"output_tokens":1},"routing":{"model":"english"}},
			{"model":"laya","answers":{"relevant":{"type":"noul","noul":0.2,"answer_confidence":0.2}},"usage":{"input_tokens":40,"output_tokens":1},"routing":{"model":"english"}}],
			"total_usage":{"input_tokens":120,"output_tokens":3}}`))
	})
	chunks := []dto.Chunk{
		{ChunkKey: "a.md#0", Path: "/a.md", Text: "keep me"},
		{ChunkKey: "b.md#0", Path: "/b.md", Text: "drop me"},
		{ChunkKey: "c.md#0", Path: "/c.md", Text: "uncertain -> keep"},
	}
	g := c.Gate(context.Background(), dto.DecisionGateInput{Request: "q", Chunks: chunks, Threshold: 0.5})
	if g.Degraded {
		t.Fatalf("unexpected degrade: %s", g.Reason)
	}
	if len(g.Chunks) != 3 {
		t.Fatalf("chunks = %d, want 3", len(g.Chunks))
	}
	if !g.Chunks[0].Keep || g.Chunks[1].Keep || !g.Chunks[2].Keep {
		t.Fatalf("keep decisions = %+v", g.Chunks)
	}
	if g.Chunks[1].Reason != "below-threshold" || g.Chunks[2].Reason != "low-confidence-kept" {
		t.Fatalf("reasons = %q / %q", g.Chunks[1].Reason, g.Chunks[2].Reason)
	}
	if g.Checkpoint != "english" || g.PromptTokens != 120 {
		t.Fatalf("gate meta = %+v", g)
	}
}

func TestGateDegradesOnResultMismatch(t *testing.T) {
	_, c := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"results":[{"answers":{"relevant":{"noul":0.9}}}],"total_usage":{}}`))
	})
	g := c.Gate(context.Background(), dto.DecisionGateInput{
		Request:   "q",
		Chunks:    []dto.Chunk{{ChunkKey: "a#0"}, {ChunkKey: "b#0"}},
		Threshold: 0.5,
	})
	if !g.Degraded || g.Reason != dto.DecisionDegradedProtocol {
		t.Fatalf("gate = %+v, want degraded protocol", g)
	}
	if len(g.Chunks) != 0 {
		t.Fatalf("degraded gate should carry no chunk decisions")
	}
}

func TestGateSkipsEmptyInput(t *testing.T) {
	_, c := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("no HTTP call expected for an empty candidate set")
	})
	g := c.Gate(context.Background(), dto.DecisionGateInput{Request: "q", Threshold: 0.5})
	if g.Degraded || g.Chunks != nil {
		t.Fatalf("gate = %+v, want empty non-degraded", g)
	}
}

func TestPlanTimeoutDegrades(t *testing.T) {
	srv, c := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
	})
	_ = srv
	// Rebuild with a tiny timeout to force a deadline.
	c = New(fakeResolver{base: srv.URL + "/v1"}, "laya", time.Millisecond)
	plan := c.Plan(context.Background(), dto.DecisionPlannerInput{Request: "x"})
	if !plan.Degraded || plan.Reason != dto.DecisionDegradedTimeout {
		t.Fatalf("plan = %+v, want degraded timeout", plan)
	}
}
