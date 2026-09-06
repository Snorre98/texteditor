package main

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"testing"

	"texteditor/internal/routergate"
	"texteditor/internal/tool"
)

// TestToolHashMatchesToolSetHash is the D1 drift guard: `cmd/toolhash` output's
// `hash` must equal routergate.ToolSetHash over the same registry, and its
// `tools` vocabulary must be the name-sorted (name, description, parameters)
// set the fine-tune trains against. Reimplementing the hash in the emitted
// output *is* the router-tools-stale drift class (ADR-0028 §4).
func TestToolHashMatchesToolSetHash(t *testing.T) {
	reg, _, err := tool.Load()
	if err != nil {
		t.Fatalf("tool.Load: %v", err)
	}
	wantHash := routergate.ToolSetHash(reg.List())

	cmd := exec.Command("go", "run", "./cmd/toolhash")
	cmd.Dir = filepath.Join("..", "..") // module root (server/) is two levels up from this test's package dir
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			t.Fatalf("toolhash exited %v: %s", ee, ee.Stderr)
		}
		t.Fatalf("run toolhash: %v", err)
	}

	var got struct {
		Hash  string `json:"hash"`
		Tools []struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			Parameters  json.RawMessage `json:"parameters"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &got); err != nil {
		t.Fatalf("toolhash output did not parse as JSON: %v\n%s", err, out)
	}

	if got.Hash != wantHash {
		t.Fatalf("toolhash hash %q != ToolSetHash %q", got.Hash, wantHash)
	}

	want := reg.List()
	if len(got.Tools) != len(want) {
		t.Fatalf("toolhash tools count %d != registry %d", len(got.Tools), len(want))
	}
	// canonJSON compacts JSON through a round-trip — the tool registry validates
	// schemas at load, so the emitted (indented) parameters are canonically
	// identical to the embedded source; compare on that basis (the hash itself
	// is whitespace/key-order-insensitive, routergate.canonicalJSON).
	canonJSON := func(raw json.RawMessage) string {
		var v interface{}
		if err := json.Unmarshal(raw, &v); err != nil {
			return string(raw)
		}
		b, _ := json.Marshal(v)
		return string(b)
	}

	for i, d := range want {
		g := got.Tools[i]
		if g.Name != d.Name {
			t.Fatalf("tool %d name %q != %q (not name-sorted?)", i, g.Name, d.Name)
		}
		if g.Description != d.Description {
			t.Fatalf("tool %q description drift", d.Name)
		}
		if canonJSON(g.Parameters) != canonJSON(d.Parameters) {
			t.Fatalf("tool %q parameters drift", d.Name)
		}
	}
}
