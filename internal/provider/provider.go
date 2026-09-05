// Package provider holds the Provider gateway — a pure REST/SSE leaf (ADR-0016
// §2). It takes an already-resolved Target (never a name) and speaks
// OpenAI-compatible /v1 to it: chat/completions, chat streaming (SSE), and
// embeddings. It emits only raw token/done/error events; attribution is
// downstream. Retry/backoff (failure-semantics §1) and per-server serialization
// are hidden internals.
package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"texteditor/shared/dto"
)

// ProviderGateway is the Provider gateway public API (interface.md §2).
type ProviderGateway interface {
	Chat(ctx context.Context, target dto.Target, req dto.Request) (dto.Completion, error)
	Stream(ctx context.Context, target dto.Target, req dto.Request, emit func(dto.RawEvent)) error
	Embed(ctx context.Context, target dto.Target, text string) ([]float32, error)
}

// Interface is an alias for ProviderGateway (the contracted name, interface.md §2).
type Interface = ProviderGateway

// ErrProviderUnreachable is surfaced when the server is down/unreachable
// (failure-semantics §2).
var ErrProviderUnreachable = errors.New("provider-unreachable")

// gateway is the concrete Provider. Pure REST leaf; no weights, no name
// resolution (ADR-0016 §2).
type gateway struct {
	client *http.Client
	mu     sync.Mutex
	queues map[string]*sync.Mutex // per-server -np 1 serialization
}

// New returns a Provider gateway.
func New() ProviderGateway {
	return &gateway{
		client: &http.Client{Timeout: 0}, // streaming turns may run long
		queues: map[string]*sync.Mutex{},
	}
}

// NewWithClient returns a Provider over a caller-supplied client (tests).
func NewWithClient(c *http.Client) ProviderGateway {
	return &gateway{client: c, queues: map[string]*sync.Mutex{}}
}

func (g *gateway) queueFor(baseURL string) *sync.Mutex {
	g.mu.Lock()
	defer g.mu.Unlock()
	m, ok := g.queues[baseURL]
	if !ok {
		m = &sync.Mutex{}
		g.queues[baseURL] = m
	}
	return m
}

// Chat performs a non-streaming chat completion. It renders the assembled
// dto.Request to the OpenAI-compatible wire format and posts it. The Provider is
// a pure transport: the request (messages, tools, model name, merged params) is
// built upstream by the Context assembler (ADR-0011) and carried intact
// (interface.md §2/§5; the §2 gap is closed via dto.Request).
func (g *gateway) Chat(ctx context.Context, target dto.Target, req dto.Request) (dto.Completion, error) {
	body := renderBody(req, false)
	raw, err := g.do(ctx, target, "chat/completions", body, false)
	if err != nil {
		return dto.Completion{}, err
	}
	return parseCompletion(raw)
}

// Stream performs a streaming chat completion, emitting one dto.RawEvent per SSE
// frame (token/done/error). The emitted events are raw and un-attributed.
func (g *gateway) Stream(ctx context.Context, target dto.Target, req dto.Request, emit func(dto.RawEvent)) error {
	body := renderBody(req, true)
	return g.stream(ctx, target, "chat/completions", body, emit)
}

// Embed returns the embedding vector for text.
func (g *gateway) Embed(ctx context.Context, target dto.Target, text string) ([]float32, error) {
	body := map[string]interface{}{
		"model": "local",
		"input": text,
	}
	raw, err := g.do(ctx, target, "embeddings", body, false)
	if err != nil {
		return nil, err
	}
	var rsp struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &rsp); err != nil {
		return nil, err
	}
	if len(rsp.Data) == 0 {
		return nil, errors.New("embed response has no data")
	}
	return rsp.Data[0].Embedding, nil
}

// renderBody renders an assembled dto.Request to the OpenAI-compatible request
// body. The Provider owns only the wire format; the content (messages, tools,
// serving model, merged params) arrives fully-assembled from the Context
// assembler upstream (ADR-0011).
func renderBody(req dto.Request, stream bool) map[string]interface{} {
	body := map[string]interface{}{
		"model":       req.ModelName,
		"messages":    toOpenAIMessages(req.Messages),
		"temperature": req.EffectiveParams.Temperature,
		"max_tokens":  req.EffectiveParams.MaxTokens,
	}
	if stream {
		body["stream"] = true
	}
	if len(req.Tools) > 0 {
		tools := make([]map[string]interface{}, 0, len(req.Tools))
		for _, t := range req.Tools {
			tools = append(tools, map[string]interface{}{
				"type": "function",
				"function": map[string]interface{}{
					"name":        t.Name,
					"description": t.Description,
					"parameters":  t.Parameters,
				},
			})
		}
		body["tools"] = tools
	}
	return body
}

// toOpenAIMessages maps assembled messages to the wire {role, content} form.
func toOpenAIMessages(msgs []dto.Message) []map[string]string {
	out := make([]map[string]string, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, map[string]string{"role": m.Role, "content": m.Content})
	}
	return out
}

// do serializes per-server and performs a non-streaming POST with retry/backoff.
func (g *gateway) do(ctx context.Context, target dto.Target, path string, body interface{}, stream bool) ([]byte, error) {
	q := g.queueFor(target.BaseURL)
	q.Lock()
	defer q.Unlock()

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	var lastErr error
	for attempt := 0; attempt <= 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff(attempt)):
			}
		}

		raw, status, err := g.post(ctx, target, path, payload)
		if err != nil {
			lastErr = err
			continue
		}
		if status >= 200 && status < 300 {
			return raw, nil
		}
		if status >= 400 && status < 500 {
			return nil, fmt.Errorf("provider 4xx (%d): %s", status, trunc(raw))
		}
		lastErr = fmt.Errorf("provider 5xx (%d)", status)
	}
	return nil, fmt.Errorf("%w: %v", ErrProviderUnreachable, lastErr)
}

// stream performs a streaming POST with retry/backoff, feeding SSE frames to emit.
func (g *gateway) stream(ctx context.Context, target dto.Target, path string, body interface{}, emit func(dto.RawEvent)) error {
	q := g.queueFor(target.BaseURL)
	q.Lock()
	defer q.Unlock()

	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}

	for attempt := 0; attempt <= 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff(attempt)):
			}
		}
		err := g.streamOnce(ctx, target, path, payload, emit)
		if err == nil {
			return nil
		}
		var pe *providerError
		if errors.As(err, &pe) && pe.status >= 400 && pe.status < 500 {
			return err // 4xx: never retried
		}
	}
	return ErrProviderUnreachable
}

type providerError struct {
	status int
	msg    string
}

func (e *providerError) Error() string { return e.msg }

func (g *gateway) streamOnce(ctx context.Context, target dto.Target, path string, payload []byte, emit func(dto.RawEvent)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.BaseURL+"/"+strings.TrimPrefix(path, "/"), bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	resp, err := g.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 && resp.StatusCode < 600 {
		return &providerError{status: resp.StatusCode, msg: fmt.Sprintf("provider %d", resp.StatusCode)}
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 1024*64), 1024*1024)
	p := &frameParser{}
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			continue
		}
		// A frame may yield zero or more raw events (a tool-call frame emits a
		// tool_call event per completed call; a final frame emits finish).
		for _, ev := range p.frame(data) {
			emit(ev)
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	// Flush any tool calls that complete at end-of-stream without an explicit
	// finish frame (defensive; OpenAI servers always send finish_reason).
	for _, ev := range p.flush() {
		emit(ev)
	}
	return nil
}

// frameParser accumulates streaming tool-call deltas and converts each SSE data
// frame into zero or more raw events (token | tool_call | finish | done). The
// tool-call shapes are pinned in interface.md §2 (amended at the agentic-loop
// milestone): tool_call → {"id","name","arguments"}, finish → {"reason"}.
type frameParser struct {
	toolCalls map[int]toolCallAcc
}

type toolCallAcc struct {
	id        string
	name      string
	arguments string
}

func (p *frameParser) frame(data string) []dto.RawEvent {
	var f struct {
		Choices []struct {
			Delta        json.RawMessage `json:"delta"`
			FinishReason string          `json:"finish_reason"`
		} `json:"choices"`
		Usage *struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal([]byte(data), &f); err != nil {
		// Unparseable frame: fall back to treating it as raw token text (the
		// legacy defensive path).
		return []dto.RawEvent{{Type: "token", Data: json.RawMessage(fmt.Sprintf(`{"text": %q}`, data))}}
	}

	var out []dto.RawEvent

	// Usage frame → done.
	if f.Usage != nil && len(f.Choices) == 0 {
		out = append(out, dto.RawEvent{
			Type: "done",
			Data: json.RawMessage(fmt.Sprintf(`{"inputTokens":%d,"outputTokens":%d}`, f.Usage.PromptTokens, f.Usage.CompletionTokens)),
		})
		return out
	}

	for _, ch := range f.Choices {
		// finish event (finish_reason tool_calls/stop/etc).
		if ch.FinishReason != "" {
			out = append(out, dto.RawEvent{
				Type: "finish",
				Data: json.RawMessage(fmt.Sprintf(`{"reason":%q}`, ch.FinishReason)),
			})
		}

		var delta struct {
			Content   string `json:"content"`
			ToolCalls []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		}
		if len(ch.Delta) > 0 {
			if err := json.Unmarshal(ch.Delta, &delta); err != nil {
				continue
			}
		}

		// Content token.
		if delta.Content != "" {
			t, _ := json.Marshal(map[string]string{"text": delta.Content})
			out = append(out, dto.RawEvent{Type: "token", Data: t})
		}

		// Tool-call deltas: accumulate by index; when a call's arguments finish
		// (signaled by the stream's finish_reason), we emit it. For providers
		// that send a complete arguments blob in one delta and no finish reason,
		// emit immediately.
		for _, tc := range delta.ToolCalls {
			if p.toolCalls == nil {
				p.toolCalls = map[int]toolCallAcc{}
			}
			acc := p.toolCalls[tc.Index]
			if tc.ID != "" {
				acc.id = tc.ID
			}
			if tc.Function.Name != "" {
				acc.name = tc.Function.Name
			}
			acc.arguments += tc.Function.Arguments
			p.toolCalls[tc.Index] = acc
		}
	}

	// A finish_reason "tool_calls" completes all accumulated calls: emit each.
	for _, ch := range f.Choices {
		if ch.FinishReason == "tool_calls" {
			out = append(out, p.emitCompleted()...)
			break
		}
	}
	return out
}

// emitCompleted emits a tool_call event for every accumulated (complete) call.
func (p *frameParser) emitCompleted() []dto.RawEvent {
	// Emit in ascending index order for determinism.
	idx := make([]int, 0, len(p.toolCalls))
	for i := range p.toolCalls {
		idx = append(idx, i)
	}
	sort.Ints(idx)
	var out []dto.RawEvent
	for _, i := range idx {
		acc := p.toolCalls[i]
		b, _ := json.Marshal(map[string]string{"id": acc.id, "name": acc.name, "arguments": acc.arguments})
		out = append(out, dto.RawEvent{Type: "tool_call", Data: b})
	}
	p.toolCalls = nil
	return out
}

// flush emits any un-completed tool calls at end-of-stream.
func (p *frameParser) flush() []dto.RawEvent {
	if len(p.toolCalls) == 0 {
		return nil
	}
	return p.emitCompleted()
}

func (g *gateway) post(ctx context.Context, target dto.Target, path string, payload []byte) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.BaseURL+"/"+strings.TrimPrefix(path, "/"), bytes.NewReader(payload))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := g.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return raw, resp.StatusCode, nil
}

// parseCompletion extracts a dto.Completion from a non-streaming chat response.
func parseCompletion(raw []byte) (dto.Completion, error) {
	var rsp struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &rsp); err != nil {
		return dto.Completion{}, err
	}
	c := dto.Completion{
		InputTokens:  rsp.Usage.PromptTokens,
		OutputTokens: rsp.Usage.CompletionTokens,
	}
	if len(rsp.Choices) > 0 {
		c.Text = rsp.Choices[0].Message.Content
		for _, tc := range rsp.Choices[0].Message.ToolCalls {
			c.ToolCalls = append(c.ToolCalls, dto.ToolCall{
				ID:        tc.ID,
				Name:      tc.Function.Name,
				Arguments: tc.Function.Arguments,
			})
		}
	}
	return c, nil
}

func backoff(attempt int) time.Duration {
	d := time.Duration(250*(1<<(attempt-1))) * time.Millisecond
	if d > 2*time.Second {
		d = 2 * time.Second
	}
	return d
}

func trunc(b []byte) string {
	if len(b) > 200 {
		return string(b[:200]) + "..."
	}
	return string(b)
}
