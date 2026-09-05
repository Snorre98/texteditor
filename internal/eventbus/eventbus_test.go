package eventbus

import (
	"testing"

	"texteditor/shared/dto"
)

func TestEmitFansOut(t *testing.T) {
	b := New()
	ch1 := b.Subscribe(nil)
	ch2 := b.Subscribe(nil)

	b.Emit(dto.Event{TurnID: "t1", Type: "token", Data: []byte(`{"text":"a"}`)})

	e1 := <-ch1
	e2 := <-ch2
	if e1.Type != "token" || e2.Type != "token" {
		t.Fatalf("fan-out failed: %+v / %+v", e1, e2)
	}
	if string(e1.Data) != `{"text":"a"}` || string(e2.Data) != `{"text":"a"}` {
		t.Fatalf("payload mismatch: %s / %s", e1.Data, e2.Data)
	}
}

func TestFilter(t *testing.T) {
	b := New()
	// Only turn "want" events pass the filter.
	ch := b.Subscribe(func(e dto.Event) bool { return e.TurnID == "want" })

	b.Emit(dto.Event{TurnID: "skip", Type: "token"})
	b.Emit(dto.Event{TurnID: "want", Type: "done"})

	got := <-ch
	if got.TurnID != "want" || got.Type != "done" {
		t.Fatalf("filter leaked: %+v", got)
	}
	// Ensure nothing else is queued.
	select {
	case extra := <-ch:
		t.Fatalf("unexpected event: %+v", extra)
	default:
	}
}

func TestOverflowDropsOldestAndLabels(t *testing.T) {
	b := New()
	var snapshot = struct{ used bool }{}
	_ = snapshot
	ch := b.Subscribe(nil)

	// Fill beyond capacity (256). The first event should be dropped and replaced
	// by a backpressure marker; subsequent emits continue to drop-oldest.
	for i := 0; i <= Capacity; i++ {
		b.Emit(dto.Event{TurnID: "t", Type: "token", Data: []byte(`{}`)})
	}

	// Drain and look for at least one backpressure marker and a bounded total.
	total := 0
	backpressure := 0
	for {
		select {
		case e := <-ch:
			total++
			if e.Type == "backpressure" {
				backpressure++
			}
		default:
			goto drained
		}
	}
drained:
	if total > Capacity {
		t.Fatalf("buffer exceeded capacity: %d", total)
	}
	if backpressure != 1 {
		t.Fatalf("backpressure events = %d, want exactly 1", backpressure)
	}
}

func TestBackpressureCarriesTurnID(t *testing.T) {
	b := New()
	ch := b.Subscribe(nil)
	for i := 0; i <= Capacity; i++ {
		b.Emit(dto.Event{TurnID: "abc", Type: "token"})
	}
	var bp dto.Event
	for {
		select {
		case e := <-ch:
			if e.Type == "backpressure" {
				bp = e
				goto found
			}
		default:
			goto found
		}
	}
found:
	if bp.TurnID != "abc" {
		t.Fatalf("backpressure turnID = %q, want abc", bp.TurnID)
	}
}
