// Package fleet holds the Fleet gateway — the engine's ONLY reach into serving
// (ADR-0016 §1, ADR-0025). It owns model discovery, resolution (merge + capability
// gates + fallback ladder), and lifecycle.
//
// The Fleet gateway is the control daemon's HTTP client (ADR-0025): it reads
// serving state only through the daemon's verb contract (ADR-0007 §12) and never
// touches models.json or serve.sh directly (ADR-0025, ADR-0027). It also never
// learns runner/source/provisioning fields — those are daemon-owned (ADR-0016 §1).
//
// ============================================================================
// STUB — Plan B dependency pending (ADR-0025, ADR-0018, ADR-0007, ADR-0030)
// ============================================================================
// The daemon HTTP transport is NOT implemented yet. Plan B (macos-dev-config) is
// the counterpart track that builds the control daemon + serve.sh executor + the
// two-tier fleet manifest loader (implementation-sequence.md Plan B, A2/A3). The
// real `NewDaemon` constructor below must, when Plan B lands, implement:
//
//   - ListModels()  → daemon `list` verb → two-tier manifest (daemons + models)
//                     mapped to []dto.Model (ADR-0018 §1/§4).
//   - Status(name)  → daemon `status` verb → health(/v1/models,/api/tags) (ADR-0007).
//   - Start(name)   → daemon `start` verb; BLOCKING up-or-typed-error (60s bound;
//                     port-in-use/binary-missing/model-not-found/timeout).
//   - Stop(name)    → daemon `stop` verb (idempotent).
//   - Provision     → daemon `provision` verb (async; observable via status).
//   - Resolve       → merge manifest.defaults ← mode.params ← overrides; enforce
//                     capability gates (context budget vs contextLength, thinking);
//                     fold in the fallback ladder (ADR-0015) — see resolve() below.
//
// Until then, NewStub provides an in-memory model list so the Resolve fallback
// ladder and every consumer (Retriever, Loop) are boundary-testable against the
// sealed dto types (Q5, ADR-0022). Remove NewStub when the daemon client lands.
package fleet

import (
	"context"
	"errors"
	"sort"

	"texteditor/shared/dto"
)

// FleetGateway is the Fleet gateway public API (interface.md §1).
type FleetGateway interface {
	ListModels() ([]dto.Model, error)
	Resolve(name string, opts dto.ResolveOpts) (dto.Resolution, error)
	Status(name string) (dto.LiveState, error)
	Start(name string) error
	Stop(name string) error
	Provision(ctx context.Context, name string) (string, error)
}

// Interface is an alias for FleetGateway (the contracted name, interface.md §1).
type Interface = FleetGateway

// Typed errors surfaced by the Fleet gateway (failure-semantics §2).
var (
	ErrModelNotFound  = errors.New("model-not-found: name not in the fleet")
	ErrNoModelAvailable = errors.New("no-model-available: no servable model for the mode")
	ErrDaemonUnreachable = errors.New("daemon-unreachable: no serving control")
)

// stub implements Interface over an in-memory model list (see package note).
type stub struct {
	models map[string]dto.Model
	state  map[string]dto.LiveState
	order  []string
}

// NewStub returns a Fleet gateway over an in-memory fleet. It is the A3 boundary
// stand-in for the daemon-backed implementation (Plan B). Live states default to
// LiveUp; callers may reset them via the returned shim for fallback tests.
func NewStub(models []dto.Model) FleetGateway {
	f := &stub{models: map[string]dto.Model{}, state: map[string]dto.LiveState{}}
	for _, m := range models {
		f.models[m.Name] = m
		f.state[m.Name] = dto.LiveUp
		f.order = append(f.order, m.Name)
	}
	sort.Strings(f.order)
	return f
}

// NewDaemon is the PLACEHOLDER for the daemon HTTP client (ADR-0025). It is not
// implemented until Plan B lands (see package note). Returns ErrDaemonUnreachable
// so a consumer wired prematurely fails loudly rather than silently.
func NewDaemon(baseURL string) FleetGateway {
	return &noDaemon{}
}

type noDaemon struct{}

func (noDaemon) ListModels() ([]dto.Model, error) { return nil, ErrDaemonUnreachable }
func (noDaemon) Resolve(string, dto.ResolveOpts) (dto.Resolution, error) {
	return dto.Resolution{}, ErrDaemonUnreachable
}
func (noDaemon) Status(string) (dto.LiveState, error) { return dto.LiveUnknown, ErrDaemonUnreachable }
func (noDaemon) Start(string) error                  { return ErrDaemonUnreachable }
func (noDaemon) Stop(string) error                   { return ErrDaemonUnreachable }
func (noDaemon) Provision(context.Context, string) (string, error) {
	return "", ErrDaemonUnreachable
}

func (f *stub) ListModels() ([]dto.Model, error) {
	out := make([]dto.Model, 0, len(f.order))
	for _, name := range f.order {
		out = append(out, f.models[name])
	}
	return out, nil
}

// Resolve merges params, enforces capability gates, and folds in the fallback
// ladder (ADR-0015, interface.md §1). In the stub there are no manifest defaults,
// so EffectiveParams == opts.Overrides (the caller passes mode.params merged with
// any turn override). The daemon-backed implementation will prepend manifest
// defaults and enforce context-length/thinking gates against the served model.
func (f *stub) Resolve(name string, opts dto.ResolveOpts) (dto.Resolution, error) {
	m, ok := f.models[name]
	if !ok {
		return dto.Resolution{}, ErrModelNotFound
	}

	preferred := f.state[name]
	if preferred == dto.LiveUp {
		return dto.Resolution{
			Model:           m,
			EffectiveParams: effectiveParams(opts),
			LiveState:       preferred,
			Degraded:        false,
			UsedName:        name,
		}, nil
	}

	// Fallback ladder: walk models sharing opts.ModeTag in fleet order, first up.
	for _, cand := range f.order {
		if !hasTag(f.models[cand], opts.ModeTag) {
			continue
		}
		if f.state[cand] == dto.LiveUp {
			return dto.Resolution{
				Model:           f.models[cand],
				EffectiveParams: effectiveParams(opts),
				LiveState:       dto.LiveUp,
				Degraded:        true,
				UsedName:        cand,
			}, nil
		}
	}
	return dto.Resolution{}, ErrNoModelAvailable
}

func (f *stub) Status(name string) (dto.LiveState, error) {
	s, ok := f.state[name]
	if !ok {
		return dto.LiveUnknown, ErrModelNotFound
	}
	return s, nil
}

func (f *stub) Start(name string) error {
	if _, ok := f.models[name]; !ok {
		return ErrModelNotFound
	}
	f.state[name] = dto.LiveUp
	return nil
}

func (f *stub) Stop(name string) error {
	if _, ok := f.models[name]; !ok {
		return ErrModelNotFound
	}
	f.state[name] = dto.LiveDown
	return nil
}

func (f *stub) Provision(_ context.Context, name string) (string, error) {
	if _, ok := f.models[name]; !ok {
		return "", ErrModelNotFound
	}
	f.state[name] = dto.LiveProvisioning
	return "provision-" + name, nil
}

func effectiveParams(opts dto.ResolveOpts) dto.SamplingParams {
	if opts.Overrides != nil {
		return *opts.Overrides
	}
	return dto.SamplingParams{}
}

func hasTag(m dto.Model, tag string) bool {
	for _, t := range m.ModeTags {
		if t == tag {
			return true
		}
	}
	return false
}
