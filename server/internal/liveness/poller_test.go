package liveness

import (
	"context"
	"encoding/json"
	"testing"

	"texteditor/internal/fleet"
	"texteditor/shared/dto"
)

// stubFleet is a minimal Fleet gateway for the poller tests.
type stubFleet struct {
	models      []dto.Model
	states      map[string]dto.LiveState
	unreachable bool
}

func (s stubFleet) ListModels() ([]dto.Model, error) {
	if s.unreachable {
		return s.models, fleet.ErrDaemonUnreachable
	}
	return s.models, nil
}
func (s stubFleet) Resolve(string, dto.ResolveOpts) (dto.Resolution, error) {
	return dto.Resolution{}, nil
}
func (s stubFleet) Status(string) (dto.LiveState, error) { return dto.LiveUp, nil }
func (s stubFleet) ListStatus() ([]dto.ModelState, error) {
	out := make([]dto.ModelState, 0, len(s.models))
	for _, m := range s.models {
		st := dto.LiveUp
		if s.unreachable {
			st = dto.LiveUnknown
		} else if v, ok := s.states[m.Name]; ok {
			st = v
		}
		out = append(out, dto.ModelState{Name: m.Name, State: st})
	}
	if s.unreachable {
		return out, fleet.ErrDaemonUnreachable
	}
	return out, nil
}
func (s stubFleet) Start(string) error                                { return nil }
func (s stubFleet) Stop(string) error                                 { return nil }
func (s stubFleet) Provision(context.Context, string) (string, error) { return "", nil }
func (s stubFleet) Fingerprint(string) (string, error)                { return "", nil }

type recBus struct{ events []dto.Event }

func (b *recBus) Emit(ev dto.Event) { b.events = append(b.events, ev) }

func TestPollerEmitsOnlyOnChange(t *testing.T) {
	f := stubFleet{
		models: []dto.Model{{Name: "a", BaseURL: "http://x/v1"}},
		states: map[string]dto.LiveState{"a": dto.LiveDown},
	}
	bus := &recBus{}
	p := New(Options{Fleet: f, Bus: bus})

	p.poll()
	if len(bus.events) != 1 {
		t.Fatalf("first poll events = %d, want 1", len(bus.events))
	}
	p.poll()
	if len(bus.events) != 1 {
		t.Fatalf("unchanged projection emitted again: %d", len(bus.events))
	}

	f.states["a"] = dto.LiveUp
	p.opts.Fleet = f
	p.poll()
	if len(bus.events) != 2 {
		t.Fatalf("changed projection events = %d, want 2", len(bus.events))
	}
	var payload dto.FleetEventPayload
	if err := json.Unmarshal(bus.events[1].Data, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Control != "up" || payload.Models[0].LiveState != dto.LiveUp {
		t.Fatalf("payload = %+v", payload)
	}
}

func TestSnapshotFoldsDaemonOutage(t *testing.T) {
	f := stubFleet{models: []dto.Model{{Name: "a", BaseURL: "http://x/v1"}}, unreachable: true}
	snap, err := Snapshot(f)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Control != "unreachable" {
		t.Fatalf("control = %q, want unreachable", snap.Control)
	}
	if snap.Models[0].LiveState != dto.LiveUnknown {
		t.Fatalf("liveState = %q, want unknown", snap.Models[0].LiveState)
	}
}
