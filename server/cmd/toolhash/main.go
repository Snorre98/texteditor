// Command toolhash emits the canonical tool-set fingerprint + vocabulary that
// the Needle router fine-tune consumes (ADR-0028 §4, D1). It loads the tool
// registry (tool.Load() — pure embedded config, no daemon, no serving) and
// prints JSON `{ "hash", "tools" }`, reusing the exact hash the composition
// root already computes for the routergate startup gates:
//
//	hash  := routergate.ToolSetHash(registry.List())
//
// This is the single drift-free source of the tool-set hash: the fine-tune
// must consume it from here (never recompute it — reimplementing the hash *is*
// the router-tools-stale drift class ADR-0028 §4 forbids). The tool vocabulary
// (name, description, parameters — sorted by name) is what the fine-tune is
// trained against, so it ships alongside the hash in one output.
//
// No CGO, no serving: this is a pure-config leaf binary (ADR-0003).
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"texteditor/internal/routergate"
	"texteditor/internal/tool"
)

// toolEntry is one tool in the vocabulary emitted for the fine-tune.
type toolEntry struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "toolhash: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	registry, _, err := tool.Load()
	if err != nil {
		return fmt.Errorf("load tools: %w", err)
	}

	defs := registry.List() // already name-sorted (tool.registry.List)

	tools := make([]toolEntry, 0, len(defs))
	for _, d := range defs {
		tools = append(tools, toolEntry{Name: d.Name, Description: d.Description, Parameters: d.Parameters})
	}
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })

	out := struct {
		Hash  string      `json:"hash"`
		Tools []toolEntry `json:"tools"`
	}{
		Hash:  routergate.ToolSetHash(defs),
		Tools: tools,
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}
