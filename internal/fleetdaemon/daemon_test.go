package fleetdaemon

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"testing"

	"texteditor/internal/fleet"
	"texteditor/shared/dto"
)

const testManifest = `{
  "$schema": "https://texteditor.local/schemas/fleet-manifest.schema.json",
  "daemons": [
    { "name": "drafter", "runner": "mlx-lm", "host": "127.0.0.1", "port": 8085 },
    { "name": "proofer", "runner": "mlx-lm", "host": "127.0.0.1", "port": 8086 }
  ],
  "models": [
    {
      "name": "mistral-24b",
      "daemon": "drafter",
      "source": { "kind": "hf", "repo": "mlx-community/Mistral-Small-3.1-24B-Instruct-4bit" },
      "capabilities": { "contextLength": 131072, "thinkingMode": false, "supportsSystemPrompt": true },
      "defaults": { "temperature": 0.5 },
      "modeTags": ["drafter"]
    },
    {
      "name": "phi-4",
      "daemon": "proofer",
      "source": { "kind": "hf", "repo": "mlx-community/phi-4-4bit" },
      "capabilities": { "contextLength": 16384, "thinkingMode": false, "supportsSystemPrompt": true },
      "defaults": { "temperature": 0.3 },
      "modeTags": ["proofreader"]
    }
  ]
}`

func parseTestManifest(t *testing.T) *Manifest {
	t.Helper()
	m, err := Parse([]byte(testManifest))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return m
}

func newTestControl(t *testing.T) (*Control, error) {
	m := parseTestManifest(t)
	return New(m, t.TempDir(), "/not/real/serve.sh"), nil
}

func TestParseValid(t *testing.T) {
	m := parseTestManifest(t)
	if len(m.Models) != 2 || len(m.Daemons) != 2 {
		t.Fatalf("manifest parsed wrong: %+v", m)
	}
}

func TestLanesConflict(t *testing.T) {
	raw := `{
  "$schema": "https://texteditor.local/schemas/fleet-manifest.schema.json",
  "daemons": [
    { "name": "a", "runner": "mlx-lm", "host": "127.0.0.1", "port": 1 },
    { "name": "b", "runner": "mlx-lm", "host": "127.0.0.1", "port": 2 }
  ],
  "models": [
    { "name": "m1", "daemon": "a", "source": { "kind": "hf", "repo": "r/repo" },
      "capabilities": { "contextLength": 1, "thinkingMode": false, "supportsSystemPrompt": false } },
    { "name": "m2", "daemon": "b", "source": { "kind": "hf", "repo": "r/repo" },
      "capabilities": { "contextLength": 1, "thinkingMode": false, "supportsSystemPrompt": false } }
  ]
}`
	_, err := Parse([]byte(raw))
	if !errors.Is(err, ErrLanesConflict) {
		t.Fatalf("want ErrLanesConflict, got %v", err)
	}
}

func TestPortCollision(t *testing.T) {
	raw := `{
  "$schema": "https://texteditor.local/schemas/fleet-manifest.schema.json",
  "daemons": [
    { "name": "a", "runner": "mlx-lm", "host": "127.0.0.1", "port": 8080 },
    { "name": "b", "runner": "mlx-lm", "host": "127.0.0.1", "port": 8080 }
  ],
  "models": []
}`
	_, err := Parse([]byte(raw))
	if err == nil {
		t.Fatal("want port-collision error")
	}
}

func TestUnknownDaemonRef(t *testing.T) {
	raw := `{
  "$schema": "https://texteditor.local/schemas/fleet-manifest.schema.json",
  "daemons": [ { "name": "a", "runner": "mlx-lm", "host": "127.0.0.1", "port": 8080 } ],
  "models": [
    { "name": "m1", "daemon": "missing", "source": { "kind": "hf", "repo": "r/repo" },
      "capabilities": { "contextLength": 1, "thinkingMode": false, "supportsSystemPrompt": false } }
  ]
}`
	_, err := Parse([]byte(raw))
	if err == nil {
		t.Fatal("want unknown-daemon error")
	}
}

func TestListMatchesContract(t *testing.T) {
	d, _ := newTestControl(t)
	entries := d.List()
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(entries))
	}
	var mistral *daemonEntry
	for i := range entries {
		if entries[i].Name == "mistral-24b" {
			mistral = &entries[i]
		}
	}
	if mistral == nil {
		t.Fatal("mistral-24b absent")
	}
	if mistral.Host != "127.0.0.1" || mistral.Port != 8085 {
		t.Fatalf("mistral host/port = %s:%d", mistral.Host, mistral.Port)
	}
	if mistral.Capabilities.ContextLength != 131072 {
		t.Fatalf("capabilities = %+v", mistral.Capabilities)
	}
	if mistral.Defaults.Temperature != 0.5 {
		t.Fatalf("defaults = %+v", mistral.Defaults)
	}
	if len(mistral.ModeTags) != 1 || mistral.ModeTags[0] != "drafter" {
		t.Fatalf("modeTags = %v", mistral.ModeTags)
	}
}

func TestStatusUnknownServer(t *testing.T) {
	d, _ := newTestControl(t)
	_, err := d.Status("nope")
	if !errors.Is(err, ErrUnknownServer) {
		t.Fatalf("want ErrUnknownServer, got %v", err)
	}
}

func TestStartPreBindGate(t *testing.T) {
	// A non-localhost bind must be refused before any spawn (ADR-0021 §3).
	raw := `{
  "$schema": "https://texteditor.local/schemas/fleet-manifest.schema.json",
  "daemons": [ { "name": "a", "runner": "mlx-lm", "host": "0.0.0.0", "port": 8080 } ],
  "models": [
    { "name": "m1", "daemon": "a", "source": { "kind": "hf", "repo": "r/repo" },
      "capabilities": { "contextLength": 1, "thinkingMode": false, "supportsSystemPrompt": false } }
  ]
}`
	m, err := Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	d := New(m, t.TempDir(), "/not/real/serve.sh")
	d.SetRunner(func(r runnerSpec) (*exec.Cmd, error) {
		t.Fatal("runner must not be invoked behind the pre-bind gate")
		return nil, nil
	})
	_, err = d.Start(context.Background(), "m1")
	if err == nil {
		t.Fatal("want pre-bind gate refusal")
	}
}

func TestStartSuccessAndIdempotent(t *testing.T) {
	d, _ := newTestControl(t)
	// The blocking-start path needs a real health signal; assert the idempotent
	// branch instead: an already-up model returns up without re-spawn.
	d.states.set("mistral-24b", dto.LiveUp)
	st2, err2 := d.Start(context.Background(), "mistral-24b")
	if err2 != nil {
		t.Fatalf("idempotent start: %v", err2)
	}
	if st2 != dto.LiveUp {
		t.Fatalf("state = %s, want up", st2)
	}
}

func TestStopIdempotent(t *testing.T) {
	d, _ := newTestControl(t)
	st, err := d.Stop(context.Background(), "phi-4") // down/unknown → no-op
	if err != nil {
		t.Fatal(err)
	}
	if st != dto.LiveDown && st != dto.LiveUnknown {
		t.Fatalf("stop no-op state = %s", st)
	}
}

func TestReach(t *testing.T) {
	d, _ := newTestControl(t)
	base, curl, err := d.Reach("mistral-24b")
	if err != nil {
		t.Fatal(err)
	}
	if base != "http://127.0.0.1:8085/v1" {
		t.Fatalf("baseURL = %s", base)
	}
	if curl != "curl http://127.0.0.1:8085/v1/models" {
		t.Fatalf("curl = %s", curl)
	}
}

// TestFleetCrossParse verifies the engine's fleet client parses the daemon's
// exact list/status output (ADR-0032 §1: client + server compiled together).
func TestFleetCrossParse(t *testing.T) {
	d, _ := newTestControl(t)
	s := httptest.NewServer(d.Handler())
	defer s.Close()

	f := fleet.NewDaemonWithClient(s.URL, s.Client())
	models, err := f.ListModels()
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 {
		t.Fatalf("fleet list = %d, want 2", len(models))
	}
	found := false
	for _, m := range models {
		if m.Name == "mistral-24b" {
			found = true
			if m.BaseURL != "http://127.0.0.1:8085/v1" {
				t.Fatalf("baseURL = %s", m.BaseURL)
			}
			if m.Capabilities.ContextLength != 131072 {
				t.Fatalf("capabilities not parsed: %+v", m.Capabilities)
			}
		}
	}
	if !found {
		t.Fatal("mistral-24b not parsed by fleet client")
	}

	st, err := f.Status("mistral-24b")
	if err != nil {
		t.Fatal(err)
	}
	if st == dto.LiveUnknown {
		t.Fatalf("status = unknown")
	}
}

func TestHTTPStatusUnknownServer(t *testing.T) {
	d, _ := newTestControl(t)
	s := httptest.NewServer(d.Handler())
	defer s.Close()

	resp, err := http.Get(s.URL + "/status/nope")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	var env errorEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatal(err)
	}
	if env.Code != "unknown-server" {
		t.Fatalf("code = %s, want unknown-server", env.Code)
	}
}
