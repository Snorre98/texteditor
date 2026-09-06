// Package meter holds the Token metering module — counts + attribution +
// persistence (ADR-0016 §7, ADR-0022, ADR-0024). It scales the assembler's
// deterministic Breakdown onto the provider's exact counts (Q1: the scaled sum
// equals the prompt_eval_count), persists meter_events rows tagged with
// session_id + model, and emits one meter event to the bus. It owns
// thinking-token reconciliation (exact when reported, labeled approximation
// otherwise).
package meter

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"texteditor/internal/sqlmigrate"
	"texteditor/shared/dto"
)

// TokenMeter is the Token metering public API (interface.md §6, amended at A4:
// Attribute now carries sessionID + model, resolving the data-model.md §1.3
// requirement that meter_events.session_id and .model are populated).
type TokenMeter interface {
	Attribute(ctx context.Context, turnID, sessionID, model string, b dto.Breakdown, counts dto.ProviderCounts) (dto.AttributedBreakdown, error)
	// SessionUsage returns a session's cumulative token total (prompt + completion
	// across all its turns). It backs the per-session budget check (ADR-0026 §5) —
	// the meter owns the cumulative tally, so the loop reads it from here.
	SessionUsage(ctx context.Context, sessionID string) (int, error)
}

// Interface is an alias for TokenMeter (the contracted name, interface.md §6).
type Interface = TokenMeter

// Emitter is the sealed subset of the SSE event bus the meter needs: fan-out of
// the single meter event per turn (interface.md §11). Subscription is the bus's
// concern, not the meter's. Wired to the real EventBus at the composition root.
type Emitter interface {
	Emit(ev dto.Event)
}

// ErrSessionBudgetExceeded is surfaced when a turn would cross a session's
// cumulative token cap (ADR-0026 §5, failure-semantics §5). It is the loop's
// caller-visible error, not a schema-internal state.
var ErrSessionBudgetExceeded = errors.New("session-budget-exceeded: the turn would cross the session's cumulative token cap")

// meter is the concrete Token metering module. meter.db is its single-writer
// file (ADR-0016).
type meter struct {
	db  *sql.DB
	bus Emitter
}

// New returns a Token meter over an already-migrated meter.db.
func New(db *sql.DB, bus Emitter) TokenMeter {
	return &meter{db: db, bus: bus}
}

// Migrate applies the meter schema (exposed for the composition root).
func Migrate(ctx context.Context, db *sql.DB) error {
	return sqlmigrate.Migrate(ctx, db, meterSchema)
}

// Attribute scales the breakdown onto the provider counts, persists one
// meter_events row per populated component, and emits one meter event.
func (m *meter) Attribute(ctx context.Context, turnID, sessionID, model string, b dto.Breakdown, counts dto.ProviderCounts) (dto.AttributedBreakdown, error) {
	// Scale the six prompt components onto the provider's prompt_eval_count.
	scaled := scalePrompt(b, counts.InputTokens)

	// Thinking: exact when the provider reports it; otherwise the assembler's
	// estimate, labeled as an approximation (ADR-0024).
	thinking := counts.ThinkingTokens
	approx := false
	if thinking == 0 && b.Thinking > 0 {
		thinking = b.Thinking
		approx = true
	}
	// Non-thinking completion is the residual output (never folds thinking in).
	completion := counts.OutputTokens
	if completion >= thinking {
		completion -= thinking
	} else {
		completion = 0
	}

	out := dto.AttributedBreakdown{
		SystemPrompt:   scaled[0],
		Tools:          scaled[1],
		Rag:            scaled[2],
		History:        scaled[3],
		Mentions:       scaled[4],
		User:           scaled[5],
		Thinking:       thinking,
		ThinkingApprox: approx,
	}

	if err := m.persist(ctx, turnID, sessionID, model, out, completion); err != nil {
		return out, err
	}

	if m.bus != nil {
		m.bus.Emit(meterEvent(turnID, out, completion))
	}
	return out, nil
}

// scalePrompt scales the six components proportionally onto total, with the
// largest component absorbing rounding so the scaled sum equals total exactly
// (Q1, ADR-0022).
func scalePrompt(b dto.Breakdown, total int) [6]int {
	comps := [6]int{b.SystemPrompt, b.Tools, b.Rag, b.History, b.Mentions, b.User}
	sum := 0
	for _, c := range comps {
		sum += c
	}
	var out [6]int
	if sum == 0 {
		return out
	}
	// Assign floor(scaled), then distribute the residual to the largest component.
	allocated := 0
	largest := 0
	for i, c := range comps {
		out[i] = c * total / sum
		allocated += out[i]
		if c > comps[largest] {
			largest = i
		}
	}
	out[largest] += total - allocated
	return out
}

func (m *meter) persist(ctx context.Context, turnID, sessionID, model string, a dto.AttributedBreakdown, completion int) error {
	ts := time.Now().UnixMilli()
	rows := []struct {
		component          string
		prompt, completion int
		approx             int
	}{
		{"system", a.SystemPrompt, 0, 0},
		{"tools", a.Tools, 0, 0},
		{"rag", a.Rag, 0, 0},
		{"history", a.History, 0, 0},
		{"mentions", a.Mentions, 0, 0},
		{"user", a.User, 0, 0},
		{"thinking", 0, a.Thinking, boolToInt(a.ThinkingApprox)},
		// "completion" records the turn's non-thinking output tokens (the answer);
		// it has no prompt component — data-model.md §1.3's completion_tokens.
		{"completion", 0, completion, 0},
	}
	for _, r := range rows {
		if r.prompt == 0 && r.completion == 0 {
			// Skip empty components (e.g. no RAG) so rows are meaningful.
			continue
		}
		if _, err := m.db.ExecContext(ctx,
			`INSERT INTO meter_events (ts, session_id, turn_id, component, prompt_tokens, completion_tokens, approx, model)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			ts, sessionID, turnID, r.component, r.prompt, r.completion, r.approx, model,
		); err != nil {
			return err
		}
	}
	return nil
}

// SessionUsage returns the cumulative token count for a session across all its
// turns (sum of prompt_tokens + completion_tokens in meter_events). It backs the
// per-session budget check (ADR-0026 §5). Consumed via a narrow seam by the loop.
func (m *meter) SessionUsage(ctx context.Context, sessionID string) (int, error) {
	var total int
	err := m.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(prompt_tokens), 0) + COALESCE(SUM(completion_tokens), 0)
		 FROM meter_events WHERE session_id = ?`, sessionID,
	).Scan(&total)
	if err != nil {
		return 0, err
	}
	return total, nil
}

// SessionExceeded reports whether adding nextTokens to a session's cumulative
// usage would cross its budget (nil budget = unbounded). ADR-0026 §5.
func SessionExceeded(used int, budget *int, nextTokens int) bool {
	if budget == nil {
		return false
	}
	return used+nextTokens > *budget
}

// meterEvent renders the single meter event emitted per turn.
func meterEvent(turnID string, a dto.AttributedBreakdown, completion int) dto.Event {
	data, _ := json.Marshal(map[string]interface{}{
		"system":         a.SystemPrompt,
		"tools":          a.Tools,
		"rag":            a.Rag,
		"history":        a.History,
		"mentions":       a.Mentions,
		"user":           a.User,
		"thinking":       a.Thinking,
		"thinkingApprox": a.ThinkingApprox,
		"completion":     completion,
	})
	return dto.Event{TurnID: turnID, Type: "meter", Data: data}
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
