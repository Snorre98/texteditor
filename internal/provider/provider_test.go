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
