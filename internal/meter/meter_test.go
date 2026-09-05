package meter

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"

	"texteditor/internal/sqlmigrate"
	"texteditor/shared/dto"
)

type stubBus struct {
	events []dto.Event
}

func (s *stubBus) Emit(ev dto.Event) { s.events = append(s.events, ev) }

func newTestMeter(t *testing.T) (Interface, *stubBus, *sql.DB) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := sqlmigrate.Migrate(context.Background(), db, meterSchema); err != nil {
		t.Fatal(err)
	}
	bus := &stubBus{}
	return New(db, bus), bus, db
}

func TestAttributeScalesToTotal(t *testing.T) {
	m, bus, db := newTestMeter(t)

	b := dto.Breakdown{
		SystemPrompt: 40,
		Tools:        20,
		Rag:          20,
		History:      10,
		User:         10,
		Thinking:     0,
	}
	counts := dto.ProviderCounts{InputTokens: 100, OutputTokens: 50}
	a, err := m.Attribute(context.Background(), "t1", "s1", "gemma4-12b", b, counts)
	if err != nil {
		t.Fatal(err)
	}

	// Scaled sum equals the provider's prompt total exactly (Q1).
	sum := a.SystemPrompt + a.Tools + a.Rag + a.History + a.User
	if sum != 100 {
		t.Fatalf("scaled prompt sum = %d, want 100", sum)
	}

	// One meter event emitted.
	if len(bus.events) != 1 || bus.events[0].Type != "meter" {
		t.Fatalf("bus events = %+v", bus.events)
	}

	// meter_events rows persisted.
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM meter_events WHERE turn_id = 't1'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 5 { // system, tools, rag, history, user (thinking=0 skipped)
		t.Fatalf("meter_events = %d, want 5", n)
	}
}

func TestThinkingExactVsApprox(t *testing.T) {
	m, _, db := newTestMeter(t)

	b := dto.Breakdown{SystemPrompt: 10, User: 10, Thinking: 7}

	// Provider reports thinking → exact, not approx.
	a, err := m.Attribute(context.Background(), "t2", "s1", "m", b, dto.ProviderCounts{InputTokens: 20, OutputTokens: 30, ThinkingTokens: 9})
	if err != nil {
		t.Fatal(err)
	}
	if a.Thinking != 9 || a.ThinkingApprox {
		t.Fatalf("thinking = %d approx=%v, want 9 false", a.Thinking, a.ThinkingApprox)
	}

	// Provider omits thinking → assembler estimate, labeled approx.
	a2, err := m.Attribute(context.Background(), "t3", "s1", "m", b, dto.ProviderCounts{InputTokens: 20, OutputTokens: 30})
	if err != nil {
		t.Fatal(err)
	}
	if a2.Thinking != 7 || !a2.ThinkingApprox {
		t.Fatalf("thinking = %d approx=%v, want 7 true", a2.Thinking, a2.ThinkingApprox)
	}

	// The approx flag is persisted.
	var approx int
	if err := db.QueryRow(`SELECT approx FROM meter_events WHERE turn_id = 't3' AND component = 'thinking'`).Scan(&approx); err != nil {
		t.Fatal(err)
	}
	if approx != 1 {
		t.Fatalf("thinking approx flag = %d, want 1", approx)
	}
}

func TestScaleSumZeroBreakdown(t *testing.T) {
	m, _, _ := newTestMeter(t)
	a, err := m.Attribute(context.Background(), "t4", "s1", "m", dto.Breakdown{}, dto.ProviderCounts{})
	if err != nil {
		t.Fatal(err)
	}
	if a.SystemPrompt != 0 || a.User != 0 {
		t.Fatalf("zero breakdown should scale to zero: %+v", a)
	}
}
