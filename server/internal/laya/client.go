// Package laya is the sealed Laya decision-layer client (ADR-0053). It resolves
// a named Laya service via Fleet (the nomic-embed / needle-router pattern),
// calls Laya's native typed-question HTTP API — POST /v1/systemone (planner) and
// /v1/systemone/batch (gate) — and returns typed decisions. Laya is a
// non-autoregressive decision engine: the planner asks a `noul` (retrieve), a
// `choice` (thinking), and a `choice` (breadth); the gate asks one `noul` per
// candidate chunk. The engine applies the keep threshold τ. Every failure
// degrades fail-open with a labeled reason; the package never returns a fatal
// error that would fail a turn.
package laya

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"texteditor/shared/dto"
)

// DefaultModelName is the manifest model name the engine resolves by name.
const DefaultModelName = "laya"

// plannerRetrieveThreshold is the P(true) above which the planner's `noul`
// retrieve answer is taken as "retrieve" (a majority of the calibrated
// probability). The gate's keep τ is separate (DecisionPolicy.GateThreshold).
const plannerRetrieveThreshold = 0.5

// lowConfidenceFloor is the calibrated `answer_confidence` below which a gate
// answer is kept (fail-open) rather than dropped on an uncertain model. A zero
// (absent) confidence is treated as unknown, not low.
const lowConfidenceFloor = 0.5

// Resolver is the sealed Fleet subset the client needs: resolve a model by name.
type Resolver interface {
	Resolve(name string, opts dto.ResolveOpts) (dto.Resolution, error)
}

// Interface is the Laya decision-layer public API (ADR-0053).
type Interface interface {
	// Plan decides retrieve-or-not, the thinking level, and the retrieval
	// breadth for one turn. On failure it returns Degraded=true + Reason.
	Plan(ctx context.Context, in dto.DecisionPlannerInput) dto.DecisionPlan
	// Gate decides keep/drop for each candidate chunk. On failure it returns
	// Degraded=true + Reason and no chunk decisions (the caller keeps all).
	Gate(ctx context.Context, in dto.DecisionGateInput) dto.DecisionGateResult
}

type client struct {
	resolver Resolver
	http     *http.Client
	model    string
	timeout  time.Duration
}

// New builds the Laya client. model is the manifest name (DefaultModelName when
// empty); timeout bounds each call (a zero timeout uses no per-call deadline).
func New(resolver Resolver, model string, timeout time.Duration) Interface {
	if model == "" {
		model = DefaultModelName
	}
	return &client{resolver: resolver, http: &http.Client{}, model: model, timeout: timeout}
}

// withTimeout bounds a call with the configured per-call deadline.
func (c *client) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if c.timeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, c.timeout)
}

// ---------- planner ----------

func (c *client) Plan(ctx context.Context, in dto.DecisionPlannerInput) dto.DecisionPlan {
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()
	base, reason := c.resolve(ctx)
	if base == "" {
		return dto.DecisionPlan{Degraded: true, Reason: reason}
	}

	req := systemOneRequest{
		State:     plannerState(in),
		Questions: plannerQuestions(),
	}
	var resp systemOneResponse
	if err := c.post(ctx, base, "/systemone", req, &resp); err != nil {
		return dto.DecisionPlan{Degraded: true, Reason: classify(err)}
	}
	retrieve, ok := resp.Answers["retrieve"]
	if !ok {
		return dto.DecisionPlan{Degraded: true, Reason: dto.DecisionDegradedProtocol}
	}
	thinking, ok := resp.Answers["thinking"]
	if !ok {
		return dto.DecisionPlan{Degraded: true, Reason: dto.DecisionDegradedProtocol}
	}
	breadth, ok := resp.Answers["breadth"]
	if !ok {
		return dto.DecisionPlan{Degraded: true, Reason: dto.DecisionDegradedProtocol}
	}
	return dto.DecisionPlan{
		Checkpoint:       checkpoint(resp),
		Retrieve:         retrieve.Noul >= plannerRetrieveThreshold,
		Thinking:         parseThinking(thinking.Choice),
		Breadth:          parseBreadth(breadth.Choice),
		PromptTokens:     resp.Usage.InputTokens,
		CompletionTokens: resp.Usage.OutputTokens,
	}
}

// ---------- gate ----------

func (c *client) Gate(ctx context.Context, in dto.DecisionGateInput) dto.DecisionGateResult {
	res := dto.DecisionGateResult{Threshold: in.Threshold}
	if len(in.Chunks) == 0 {
		return res
	}
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()
	base, reason := c.resolve(ctx)
	if base == "" {
		res.Degraded = true
		res.Reason = reason
		return res
	}

	states := make([]string, 0, len(in.Chunks))
	for _, ch := range in.Chunks {
		states = append(states, gateState(in.Request, ch.Text))
	}
	req := systemOneBatchRequest{
		States:    states,
		Questions: gateQuestions(),
	}
	var resp systemOneBatchResponse
	if err := c.post(ctx, base, "/systemone/batch", req, &resp); err != nil {
		res.Degraded = true
		res.Reason = classify(err)
		return res
	}
	if len(resp.Results) != len(in.Chunks) {
		res.Degraded = true
		res.Reason = dto.DecisionDegradedProtocol
		return res
	}
	res.PromptTokens = resp.TotalUsage.InputTokens
	res.CompletionTokens = resp.TotalUsage.OutputTokens
	res.Chunks = make([]dto.DecisionChunkResult, 0, len(in.Chunks))
	for i, r := range resp.Results {
		a, ok := r.Answers["relevant"]
		if !ok {
			res.Degraded = true
			res.Reason = dto.DecisionDegradedProtocol
			res.Chunks = nil
			return res
		}
		if res.Checkpoint == "" {
			res.Checkpoint = checkpoint(r)
		}
		score := a.Noul
		keep := score >= in.Threshold
		chunkReason := "below-threshold"
		if keep {
			chunkReason = "kept"
		}
		// A low calibrated confidence keeps the chunk (fail-open) rather than
		// dropping it on an uncertain answer.
		if conf := a.confidence(); conf > 0 && conf < lowConfidenceFloor {
			keep = true
			chunkReason = "low-confidence-kept"
		}
		res.Chunks = append(res.Chunks, dto.DecisionChunkResult{
			ChunkKey: in.Chunks[i].ChunkKey,
			Path:     in.Chunks[i].Path,
			Score:    score,
			Keep:     keep,
			Reason:   chunkReason,
		})
	}
	return res
}

// ---------- transport ----------

// resolve returns the base URL for the Laya service, or "" plus a degrade reason.
func (c *client) resolve(ctx context.Context) (string, dto.DecisionDegradeReason) {
	res, err := c.resolver.Resolve(c.model, dto.ResolveOpts{ModeTag: c.model})
	if err != nil {
		return "", classify(err)
	}
	return strings.TrimRight(res.Model.BaseURL, "/"), ""
}

func (c *client) post(ctx context.Context, base, path string, body any, out any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+path, bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("read: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	return nil
}

// classify maps a transport/resolve error to a labeled degrade reason.
func classify(err error) dto.DecisionDegradeReason {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return dto.DecisionDegradedTimeout
	}
	// A resolved service that is down surfaces as a connection error; a resolve
	// miss surfaces as a fleet error. Both are "unreachable" to the caller.
	var netErr interface{ Timeout() bool }
	if errors.As(err, &netErr) && netErr.Timeout() {
		return dto.DecisionDegradedTimeout
	}
	return dto.DecisionDegradedUnreachable
}

// ---------- prompt construction ----------

func plannerState(in dto.DecisionPlannerInput) string {
	var b strings.Builder
	b.WriteString("Writing request:\n")
	b.WriteString(in.Request)
	if in.Mode != "" {
		b.WriteString("\n\nPreset: ")
		b.WriteString(in.Mode)
	}
	if in.Selection != "" {
		b.WriteString("\n\nSelected passage:\n")
		b.WriteString(in.Selection)
	}
	if len(in.History) > 0 {
		b.WriteString("\n\nRecent conversation:\n")
		for _, m := range in.History {
			b.WriteString("- ")
			b.WriteString(m.Role)
			b.WriteString(": ")
			b.WriteString(m.Content)
			b.WriteString("\n")
		}
	}
	return b.String()
}

func gateState(request, passage string) string {
	return "Writing request:\n" + request + "\n\nPassage:\n" + passage
}

func plannerQuestions() map[string]question {
	return map[string]question{
		"retrieve": {
			Type:         "noul",
			Instructions: "The writing request needs passages retrieved from the author's own corpus to be answered well.",
		},
		"thinking": {
			Type:         "choice",
			Instructions: "How much internal reasoning does this turn need before writing?",
			Criteria: map[string]string{
				"off":  "a mechanical, fast edit that needs no reasoning",
				"auto": "ordinary writing that may need a little reasoning",
				"on":   "a hard analytical or structural task that needs deep reasoning",
			},
		},
		"breadth": {
			Type:         "choice",
			Instructions: "How much corpus context does this turn need?",
			Criteria: map[string]string{
				"none": "no corpus context is needed",
				"few":  "a few focused passages are enough",
				"many": "broad coverage of the corpus is needed",
			},
		},
	}
}

func gateQuestions() map[string]question {
	return map[string]question{
		"relevant": {
			Type:         "noul",
			Instructions: "The passage is directly relevant and useful to the writing request.",
		},
	}
}

// ---------- parsing helpers ----------

func parseThinking(s string) dto.ThinkingLevel {
	switch dto.ThinkingLevel(strings.ToLower(strings.TrimSpace(s))) {
	case dto.ThinkingOff:
		return dto.ThinkingOff
	case dto.ThinkingOn:
		return dto.ThinkingOn
	default:
		return dto.ThinkingAuto
	}
}

func parseBreadth(s string) dto.DecisionBreadth {
	switch dto.DecisionBreadth(strings.ToLower(strings.TrimSpace(s))) {
	case dto.DecisionBreadthNone:
		return dto.DecisionBreadthNone
	case dto.DecisionBreadthMany:
		return dto.DecisionBreadthMany
	default:
		return dto.DecisionBreadthFew
	}
}

func checkpoint(r systemOneResponse) string {
	if r.Routing.Model != "" {
		return r.Routing.Model
	}
	return r.Model
}

// ---------- wire types (Laya /v1/systemone) ----------

type question struct {
	Type         string `json:"type"`
	Instructions string `json:"instructions"`
	Criteria     any    `json:"criteria,omitempty"`
}

type systemOneRequest struct {
	State     any                 `json:"state"`
	Questions map[string]question `json:"questions"`
}

type systemOneBatchRequest struct {
	States    []string            `json:"states"`
	Questions map[string]question `json:"questions"`
}

type answer struct {
	Type             string  `json:"type"`
	Choice           string  `json:"choice"`
	Score            float64 `json:"score"`
	Noul             float64 `json:"noul"`
	Confidence       float64 `json:"confidence"`
	AnswerConfidence float64 `json:"answer_confidence"`
}

// confidence prefers the calibrated answer_confidence, falling back to the
// distribution confidence when the server is in strict Jev mode.
func (a answer) confidence() float64 {
	if a.AnswerConfidence > 0 {
		return a.AnswerConfidence
	}
	return a.Confidence
}

type usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type routing struct {
	Model string `json:"model"`
}

type systemOneResponse struct {
	Model   string            `json:"model"`
	Answers map[string]answer `json:"answers"`
	Usage   usage             `json:"usage"`
	Routing routing           `json:"routing"`
}

type systemOneBatchResponse struct {
	Results    []systemOneResponse `json:"results"`
	TotalUsage usage               `json:"total_usage"`
}
