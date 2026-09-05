package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"texteditor/shared/dto"
)

// fakeDaemon is an httptest server speaking the daemon verb contract
// (contracts/daemon-http.md) backed by a fixed two-tier manifest in memory.
type fakeDaemon struct {
	t *testing.T

	mu     sync.Mutex
	models map[string]daemonEntry
	states map[string]dto.LiveState
	calls  int
}

func newFakeDaemon(t *testing.T) *fakeDaemon {
	f := &fakeDaemon{
		t:      t,
		models: map[string]daemonEntry{},
		states: map[string]dto.LiveState{},
	}
	f.add(daemonEntry{
		Name: "gemma4-12b", Host: "127.0.0.1", Port: 8001,
		Capabilities: dto.Capabilities{ContextLength: 131072},
		ModeTags:     []string{"proofreader", "editor"},
	})
	f.add(daemonEntry{
		Name: "gemma4-26b", Host: "127.0.0.1", Port: 8002,
		Capabilities: dto.Capabilities{ContextLength: 262144},
		ModeTags:     []string{"editor"},
	})
	f.add(daemonEntry{
		Name: "llama3.1-8b", Host: "127.0.0.1", Port: 8003,
		Capabilities: dto.Capabilities{ContextLength: 131072},
		ModeTags:     []string{"grammar"},
	})
	for n := range f.models {
		f.states[n] = dto.LiveUp
	}
	return f
}

func (f *fakeDaemon) add(e daemonEntry) {
	if e.Defaults.Temperature == 0 {
		e.Defaults.Temperature = 0.4
	}
	f.models[e.Name] = e
}

func (f *fakeDaemon) server() *httptest.Server {
	s := httptest.NewServer(http.HandlerFunc(f.handle))
	f.t.Cleanup(s.Close)
	return s
}

func (f *fakeDaemon) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()

	writeJSON := func(status int, v interface{}) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(v)
	}

	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/list":
		f.mu.Lock()
		entries := make([]daemonEntry, 0, len(f.models))
		for _, e := range f.models {
			entries = append(entries, e)
		}
		f.mu.Unlock()
		writeJSON(200, map[string]interface{}{"models": entries})

	case r.Method == http.MethodGet && len(r.URL.Path) > 8 && r.URL.Path[:8] == "/status/":
		name := r.URL.Path[8:]
		f.mu.Lock()
		state, ok := f.states[name]
		f.mu.Unlock()
		if !ok {
			writeJSON(404, map[string]string{"code": "unknown-server"})
			return
		}
		writeJSON(200, map[string]interface{}{"name": name, "state": string(state)})

	case r.Method == http.MethodPost && len(r.URL.Path) > 7 && r.URL.Path[:7] == "/start/":
		name := r.URL.Path[7:]
		f.mu.Lock()
		_, ok := f.models[name]
		up := f.states[name] == dto.LiveUp
		f.mu.Unlock()
		if !ok {
			writeJSON(404, map[string]string{"code": "unknown-server"})
			return
		}
		if up {
			writeJSON(400, map[string]string{"code": "already-up", "message": "already up"})
			return
		}
		f.mu.Lock()
		f.states[name] = dto.LiveUp
		f.mu.Unlock()
		writeJSON(200, map[string]string{"name": name, "state": "up"})

	case r.Method == http.MethodPost && len(r.URL.Path) > 6 && r.URL.Path[:6] == "/stop/":
		name := r.URL.Path[6:]
		f.mu.Lock()
		_, ok := f.models[name]
		f.mu.Unlock()
		if !ok {
			writeJSON(404, map[string]string{"code": "unknown-server"})
			return
		}
		f.mu.Lock()
		f.states[name] = dto.LiveDown
		f.mu.Unlock()
		writeJSON(200, map[string]string{"name": name, "state": "down"})

	case r.Method == http.MethodPost && len(r.URL.Path) > 11 && r.URL.Path[:11] == "/provision/":
		name := r.URL.Path[11:]
		f.mu.Lock()
		_, ok := f.models[name]
		f.mu.Unlock()
		if !ok {
			writeJSON(404, map[string]string{"code": "unknown-server"})
			return
		}
		f.mu.Lock()
		f.states[name] = dto.LiveProvisioning
		f.mu.Unlock()
		writeJSON(202, map[string]string{"provisionID": "prov-" + name})

	default:
		writeJSON(404, map[string]string{"code": "unknown-server"})
	}
}

func newTestFleet(t *testing.T) Interface {
	f := newFakeDaemon(t)
	return NewDaemonWithClient(f.server().URL, f.server().Client())
}

func TestResolveDirect(t *testing.T) {
	f := newTestFleet(t)
	params := dto.SamplingParams{Temperature: 0.3, MaxTokens: 10}
	res, err := f.Resolve("gemma4-12b", dto.ResolveOpts{ModeTag: "proofreader", Overrides: &params})
	if err != nil {
		t.Fatal(err)
	}
	if res.UsedName != "gemma4-12b" || res.Degraded {
		t.Fatalf("resolution = %+v, want direct", res)
	}
	if res.EffectiveParams.Temperature != 0.3 {
		t.Fatalf("effective params = %+v", res.EffectiveParams)
	}
	if res.Model.BaseURL != "http://127.0.0.1:8001/v1" {
		t.Fatalf("baseURL = %q", res.Model.BaseURL)
	}
}

func TestResolveMergesDefaults(t *testing.T) {
	f := newTestFleet(t)
	// No override: defaults.temperature (0.4) wins.
	res, err := f.Resolve("gemma4-12b", dto.ResolveOpts{ModeTag: "proofreader"})
	if err != nil {
		t.Fatal(err)
	}
	if res.EffectiveParams.Temperature != 0.4 {
		t.Fatalf("defaults not merged: %+v", res.EffectiveParams)
	}
}

func TestResolveFallbackLadder(t *testing.T) {
	tf := newFakeDaemon(t)
	tf.mu.Lock()
	tf.states["gemma4-12b"] = dto.LiveDown
	tf.mu.Unlock()
	s := tf.server()
	fl := NewDaemonWithClient(s.URL, s.Client())

	res, err := fl.Resolve("gemma4-12b", dto.ResolveOpts{ModeTag: "editor"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Degraded || res.UsedName != "gemma4-26b" {
		t.Fatalf("resolution = %+v, want degraded fallback to gemma4-26b", res)
	}
}

func TestResolveNoModelAvailable(t *testing.T) {
	tf := newFakeDaemon(t)
	tf.mu.Lock()
	tf.states["gemma4-12b"] = dto.LiveDown
	tf.states["gemma4-26b"] = dto.LiveDown
	tf.mu.Unlock()
	s := tf.server()
	f := NewDaemonWithClient(s.URL, s.Client())

	_, err := f.Resolve("gemma4-12b", dto.ResolveOpts{ModeTag: "editor"})
	if !errors.Is(err, ErrNoModelAvailable) {
		t.Fatalf("want ErrNoModelAvailable, got %v", err)
	}
}

func TestResolveUnknownModel(t *testing.T) {
	f := newTestFleet(t)
	_, err := f.Resolve("nope", dto.ResolveOpts{})
	if !errors.Is(err, ErrModelNotFound) {
		t.Fatalf("want ErrModelNotFound, got %v", err)
	}
}

func TestDaemonUnreachable(t *testing.T) {
	// Bind to a closed server: the gateway must surface daemon-unreachable, not
	// a nil deref.
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := s.URL
	s.Close()

	f := NewDaemonWithClient(url, &http.Client{})
	_, err := f.ListModels()
	if !errors.Is(err, ErrDaemonUnreachable) {
		t.Fatalf("want ErrDaemonUnreachable, got %v", err)
	}
}

func TestProvision(t *testing.T) {
	f := newTestFleet(t)
	id, err := f.Provision(context.Background(), "gemma4-12b")
	if err != nil {
		t.Fatal(err)
	}
	if id == "" {
		t.Fatal("empty provision id")
	}
}

func TestListModels(t *testing.T) {
	f := newTestFleet(t)
	models, err := f.ListModels()
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 3 {
		t.Fatalf("models = %d, want 3", len(models))
	}
}

func TestStartStop(t *testing.T) {
	tf := newFakeDaemon(t)
	tf.mu.Lock()
	tf.states["gemma4-26b"] = dto.LiveDown
	tf.mu.Unlock()
	s := tf.server()
	f := NewDaemonWithClient(s.URL, s.Client())

	if s0, _ := f.Status("gemma4-26b"); s0 != dto.LiveDown {
		t.Fatalf("status = %s, want down", s0)
	}
	if err := f.Start("gemma4-26b"); err != nil {
		t.Fatal(err)
	}
	if s1, _ := f.Status("gemma4-26b"); s1 != dto.LiveUp {
		t.Fatalf("status = %s, want up", s1)
	}
	if err := f.Stop("gemma4-26b"); err != nil {
		t.Fatal(err)
	}
	if s2, _ := f.Status("gemma4-26b"); s2 != dto.LiveDown {
		t.Fatalf("status = %s, want down", s2)
	}
	if err := f.Start("nope"); !errors.Is(err, ErrModelNotFound) {
		t.Fatalf("want ErrModelNotFound, got %v", err)
	}
}
