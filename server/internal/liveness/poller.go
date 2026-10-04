// Package liveness holds the engine-side fleet poller: the one `fleet` feed
// producer that is not a direct in-process emit (ADR-0052 §4). The control
// daemon is external (ADR-0025) and has no webhook, so the engine polls its
// batch `status/all` projection (ADR-0040 §2) on a bounded interval and emits
// a `fleet` event only when the projection changes.
package liveness

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"time"

	"texteditor/internal/fleet"
	"texteditor/shared/dto"
)

// Emitter is the sealed subset of the SSE event bus the poller needs.
type Emitter interface {
	Emit(dto.Event)
}

// Options configures the fleet poller.
type Options struct {
	Fleet fleet.Interface
	Bus   Emitter
	// Interval is the poll cadence; <= 0 disables the poller.
	Interval time.Duration
}

// Poller polls the fleet projection and emits `fleet` events on change.
type Poller struct {
	opts Options
	last []byte
}

// New returns a fleet poller.
func New(opts Options) *Poller { return &Poller{opts: opts} }

// Run polls until ctx is done. It emits a `fleet` event only when the
// projection changes (the control state or any model's live state), so the feed
// carries no heartbeat. A daemon outage is emitted as control=unreachable with
// every model state forced to unknown (ADR-0040 §3, ADR-0052 §4).
func (p *Poller) Run(ctx context.Context) {
	if p.opts.Bus == nil || p.opts.Interval <= 0 {
		return
	}
	ticker := time.NewTicker(p.opts.Interval)
	defer ticker.Stop()

	p.poll()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.poll()
		}
	}
}

// poll reads the current projection and emits a `fleet` event when it differs
// from the last emitted one.
func (p *Poller) poll() {
	payload, err := Snapshot(p.opts.Fleet)
	if err != nil {
		log.Printf("fleet poller: %v", err)
		return
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	if string(data) == string(p.last) {
		return
	}
	p.last = data
	p.opts.Bus.Emit(dto.Event{Type: dto.EventFleet, Data: data})
}

// Snapshot joins the model projection with the batch states (ADR-0040 §2),
// folding a daemon outage to control=unreachable with every state unknown. It
// is exported so the API server can build the same shape if needed.
func Snapshot(f fleet.Interface) (dto.FleetEventPayload, error) {
	models, modelsErr := f.ListModels()
	states, statesErr := f.ListStatus()

	unreachable := errors.Is(modelsErr, fleet.ErrDaemonUnreachable) ||
		errors.Is(statesErr, fleet.ErrDaemonUnreachable)
	if !unreachable {
		if modelsErr != nil {
			return dto.FleetEventPayload{}, modelsErr
		}
		if statesErr != nil {
			return dto.FleetEventPayload{}, statesErr
		}
	}

	byState := map[string]dto.LiveState{}
	if unreachable {
		for _, m := range models {
			byState[m.Name] = dto.LiveUnknown
		}
	} else {
		for _, st := range states {
			byState[st.Name] = st.State
		}
	}
	control := "up"
	if unreachable {
		control = "unreachable"
	}

	out := dto.FleetEventPayload{Control: control, Models: make([]dto.FleetEventModel, 0, len(models))}
	for _, m := range models {
		st, ok := byState[m.Name]
		if !ok {
			st = dto.LiveUnknown
		}
		out.Models = append(out.Models, dto.FleetEventModel{
			Name:         m.Name,
			BaseURL:      m.BaseURL,
			Capabilities: m.Capabilities,
			ModeTags:     m.ModeTags,
			LiveState:    st,
		})
	}
	return out, nil
}
