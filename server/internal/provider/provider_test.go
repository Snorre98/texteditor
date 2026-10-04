package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"texteditor/shared/dto"
)

func targetOf(s *httptest.Server) dto.Target {
	return dto.Target{BaseURL: s.URL}
}

func TestChat(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		w.Write([]byte(`{"choices":[{"message":{"content":"hello"}}],"usage":{"prompt_tokens":10,"completion_tokens":3}}`))
	}))
	defer s.Close()

	g := NewWithClient(s.Client())
	c, err := g.Chat(context.Background(), targetOf(s), dto.Request{ModelName: "local", EffectiveParams: dto.SamplingParams{Temperature: 0.5, MaxTokens: 100}})
	if err != nil {
		t.Fatal(err)
	}
	if c.Text != "hello" || c.InputTokens != 10 || c.OutputTokens != 3 {
		t.Fatalf("completion = %+v", c)
	}
}

// TestWireModelField: the OpenAI request body's `model` field is exactly the
// Request.ModelName the engine hands the provider (the daemon-projected wire id),
// so a provider that validates model names (e.g. mlx-lm) accepts the request.
func TestWireModelField(t *testing.T) {
	var gotModel string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		gotModel = body.Model
		w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer s.Close()

	g := NewWithClient(s.Client())
	_, err := g.Chat(context.Background(), targetOf(s), dto.Request{
		ModelName: "mlx-community/gemma-4-26B-A4B-it-OptiQ-4bit",
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotModel != "mlx-community/gemma-4-26B-A4B-it-OptiQ-4bit" {
		t.Fatalf("wire model = %q, want the request ModelName", gotModel)
	}
}

func TestEmbed(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"embedding":[0.1,0.2,0.3]}]}`))
	}))
	defer s.Close()

	g := NewWithClient(s.Client())
	v, err := g.Embed(context.Background(), targetOf(s), "some text")
	if err != nil {
		t.Fatal(err)
	}
	if len(v) != 3 || v[1] != 0.2 {
		t.Fatalf("embed = %v", v)
	}
}

func TestStreamFrames(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hel\"}}]}\n\n"))
		fl.Flush()
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"lo\"}}]}\n\n"))
		fl.Flush()
		w.Write([]byte("data: {\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":2}}\n\n"))
		fl.Flush()
		w.Write([]byte("data: [DONE]\n\n"))
		fl.Flush()
	}))
	defer s.Close()

	g := NewWithClient(s.Client())
	var events []dto.RawEvent
	err := g.Stream(context.Background(), targetOf(s), dto.Request{ModelName: "local"}, func(ev dto.RawEvent) {
		events = append(events, ev)
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("events = %d, want 3: %+v", len(events), events)
	}
	if events[0].Type != "token" || events[1].Type != "token" || events[2].Type != "done" {
		t.Fatalf("event types = %+v", events)
	}
}

func TestStreamToolCalls(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"function\":{\"name\":\"edit_markdown\",\"arguments\":\"{\\\"blockId\\\":\\\"b1\\\",\\\"text\\\":\\\"hi\\\"}\"}}]}}]}\n\n"))
		fl.Flush()
		w.Write([]byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n"))
		fl.Flush()
		w.Write([]byte("data: [DONE]\n\n"))
		fl.Flush()
	}))
	defer s.Close()

	g := NewWithClient(s.Client())
	var events []dto.RawEvent
	err := g.Stream(context.Background(), targetOf(s), dto.Request{ModelName: "local"}, func(ev dto.RawEvent) {
		events = append(events, ev)
	})
	if err != nil {
		t.Fatal(err)
	}

	var toolCall, finish bool
	for _, ev := range events {
		switch ev.Type {
		case "tool_call":
			toolCall = true
			var tc struct {
				ID        string `json:"id"`
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}
			if err := json.Unmarshal(ev.Data, &tc); err != nil {
				t.Fatal(err)
			}
			if tc.Name != "edit_markdown" || tc.ID != "call_1" || tc.Arguments == "" {
				t.Fatalf("tool_call = %+v", tc)
			}
		case "finish":
			finish = true
		}
	}
	if !toolCall || !finish {
		t.Fatalf("events = %+v", events)
	}
}

func TestChatToolCalls(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"choices":[{"finish_reason":"tool_calls","message":{"content":"","tool_calls":[{"id":"c1","function":{"name":"retrieve","arguments":"{\"query\":\"x\"}"}}]}}],"usage":{"prompt_tokens":10,"completion_tokens":3}}`))
	}))
	defer s.Close()

	g := NewWithClient(s.Client())
	c, err := g.Chat(context.Background(), targetOf(s), dto.Request{ModelName: "local"})
	if err != nil {
		t.Fatal(err)
	}
	if len(c.ToolCalls) != 1 {
		t.Fatalf("tool calls = %+v", c.ToolCalls)
	}
	if c.ToolCalls[0].Name != "retrieve" || c.ToolCalls[0].ID != "c1" {
		t.Fatalf("tool call = %+v", c.ToolCalls[0])
	}
}

func TestRetryOn5xx(t *testing.T) {
	var calls int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer s.Close()

	g := NewWithClient(s.Client())
	_, err := g.Chat(context.Background(), targetOf(s), dto.Request{ModelName: "local"})
	if err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&calls) != 3 {
		t.Fatalf("calls = %d, want 3 (2 retries)", calls)
	}
}

func TestNoRetryOn4xx(t *testing.T) {
	var calls int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer s.Close()

	g := NewWithClient(s.Client())
	_, err := g.Chat(context.Background(), targetOf(s), dto.Request{ModelName: "local"})
	if err == nil {
		t.Fatal("expected 4xx error")
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("calls = %d, want 1 (no retry on 4xx)", calls)
	}
}

// TestRenderBodyDefaultsUnsetMaxTokens: an unset budget becomes the engine
// default, never 0 — strict servers (mlx-lm >=0.32) reject 0, and their own
// 512 default truncates reasoning models before the tool call.
func TestRenderBodyDefaultsUnsetMaxTokens(t *testing.T) {
	body := renderBody(dto.Target{}, dto.Request{
		ModelName:       "m",
		Messages:        []dto.Message{{Role: "user", Content: "hi"}},
		EffectiveParams: dto.SamplingParams{Temperature: 0.3},
	}, true)
	if body["max_tokens"] != defaultMaxTokens {
		t.Fatalf("max_tokens = %v, want %d", body["max_tokens"], defaultMaxTokens)
	}
	if body["temperature"] != 0.3 || body["stream"] != true {
		t.Fatalf("body = %v", body)
	}
}

// TestRenderBodyIncludesPositiveMaxTokens: a configured budget still rides the wire.
func TestRenderBodyIncludesPositiveMaxTokens(t *testing.T) {
	body := renderBody(dto.Target{}, dto.Request{EffectiveParams: dto.SamplingParams{MaxTokens: 4096}}, false)
	if body["max_tokens"] != 4096 {
		t.Fatalf("max_tokens = %v, want 4096", body["max_tokens"])
	}
	if _, ok := body["stream"]; ok {
		t.Fatalf("stream must be omitted for a non-streaming request: %v", body)
	}
}

// TestThinkingToggleMapping: the thinking toggle is rendered per runner
// (ADR-0051 §3) — mlx-lm/llama.cpp use chat_template_kwargs, OpenAI-compatible
// uses reasoning_effort, and an unsupported runner renders nothing.
func TestThinkingToggleMapping(t *testing.T) {
	off := false
	on := true
	cases := []struct {
		runner   string
		desired  *bool
		wantKey  string
		wantVal  interface{}
		wantNone bool
	}{
		{"mlx-lm", &off, "chat_template_kwargs", map[string]interface{}{"enable_thinking": false}, false},
		{"llama.cpp", &off, "chat_template_kwargs", map[string]interface{}{"enable_thinking": false}, false},
		{"mlx-vlm", &on, "chat_template_kwargs", map[string]interface{}{"enable_thinking": true}, false},
		{"openai", &off, "reasoning_effort", "none", false},
		{"openai-compatible", &on, "reasoning_effort", "medium", false},
		{"delegate", &off, "", nil, true},
		{"", &off, "", nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.runner, func(t *testing.T) {
			body := renderBody(dto.Target{Runner: tc.runner}, dto.Request{Thinking: tc.desired}, false)
			if tc.wantNone {
				if _, ok := body["chat_template_kwargs"]; ok {
					t.Fatalf("unsupported runner must not render a toggle: %v", body)
				}
				if _, ok := body["reasoning_effort"]; ok {
					t.Fatalf("unsupported runner must not render a toggle: %v", body)
				}
				return
			}
			got, ok := body[tc.wantKey]
			if !ok {
				t.Fatalf("missing %s in %v", tc.wantKey, body)
			}
			if m, ok := tc.wantVal.(map[string]interface{}); ok {
				gm, _ := got.(map[string]interface{})
				if gm == nil || gm["enable_thinking"] != m["enable_thinking"] {
					t.Fatalf("%s = %v, want %v", tc.wantKey, got, tc.wantVal)
				}
			} else if got != tc.wantVal {
				t.Fatalf("%s = %v, want %v", tc.wantKey, got, tc.wantVal)
			}
		})
	}
}

// TestSupportsThinkingToggle: only runners with a real toggle are supported; an
// unknown/unsupported runner degrades (ADR-0051 §3).
func TestSupportsThinkingToggle(t *testing.T) {
	for _, r := range []string{"mlx-lm", "mlx-vlm", "llama.cpp", "openai"} {
		if !SupportsThinkingToggle(r) {
			t.Fatalf("SupportsThinkingToggle(%q) = false, want true", r)
		}
	}
	for _, r := range []string{"delegate", "", "unknown"} {
		if SupportsThinkingToggle(r) {
			t.Fatalf("SupportsThinkingToggle(%q) = true, want false", r)
		}
	}
}

// TestStreamReasoningDeltas: the provider surfaces reasoning deltas as raw
// `reasoning` events and the exact thinking count on `done` (ADR-0051 §4).
func TestStreamReasoningDeltas(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"think \"}}]}\n\n"))
		fl.Flush()
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"reasoning\":\"more\"}}]}\n\n"))
		fl.Flush()
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n"))
		fl.Flush()
		w.Write([]byte("data: {\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":9,\"completion_tokens_details\":{\"reasoning_tokens\":4}}}\n\n"))
		fl.Flush()
		w.Write([]byte("data: [DONE]\n\n"))
		fl.Flush()
	}))
	defer s.Close()

	g := NewWithClient(s.Client())
	var events []dto.RawEvent
	err := g.Stream(context.Background(), targetOf(s), dto.Request{ModelName: "local"}, func(ev dto.RawEvent) {
		events = append(events, ev)
	})
	if err != nil {
		t.Fatal(err)
	}
	var reasoningText string
	var thinkingTokens int
	for _, ev := range events {
		switch ev.Type {
		case "reasoning":
			var v struct {
				Text string `json:"text"`
			}
			_ = json.Unmarshal(ev.Data, &v)
			reasoningText += v.Text
		case "done":
			var v struct {
				ThinkingTokens int `json:"thinkingTokens"`
			}
			_ = json.Unmarshal(ev.Data, &v)
			thinkingTokens = v.ThinkingTokens
		}
	}
	if reasoningText != "think more" {
		t.Fatalf("reasoning text = %q, want %q", reasoningText, "think more")
	}
	if thinkingTokens != 4 {
		t.Fatalf("thinkingTokens = %d, want 4", thinkingTokens)
	}
}
