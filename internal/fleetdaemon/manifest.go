// Package fleetdaemon holds the control daemon — the HTTP transport over the
// serving lifecycle verb contract (ADR-0025, ADR-0027). It is the mirror of
// internal/fleet: the client and server of one contract (contracts/daemon-http.md)
// are authored, compiled, and CI-tested together in one module (ADR-0032).
//
// The daemon is the sole reader of models.json (ADR-0027): it parses and
// validates the two-tier manifest (structural via the committed JSON Schema;
// semantic — name/port uniqueness + the lanes rule — in this package), owns
// per-model live state, and hands the parsed manifest to serve.sh as
// per-invocation env vars (ADR-0032 §3). It binds 127.0.0.1 by default and
// enforces the pre-bind gate before any non-localhost spawn (ADR-0021 §3).
package fleetdaemon

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/santhosh-tekuri/jsonschema/v5"

	"texteditor/shared/dto"
)

// Manifest is the parsed, validated two-tier fleet manifest (data-model.md §2).
type Manifest struct {
	Schema  string   `json:"$schema"`
	Daemons []Daemon `json:"daemons"`
	Models  []Model  `json:"models"`
}

// Daemon is one lifecycle unit (data-model.md §2.2).
type Daemon struct {
	Name     string `json:"name"`
	Runner   string `json:"runner"` // llama.cpp | mlx-lm | mlx-vlm | delegate
	Delegate string `json:"delegate,omitempty"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
}

// Source is how a model is obtained/run (data-model.md §2.3, ADR-0030).
type Source struct {
	Kind        string `json:"kind"` // hf | gguf | needle
	Repo        string `json:"repo,omitempty"`
	File        string `json:"file,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
}

// Model is one resolve/provision unit (data-model.md §2.3).
type Model struct {
	Name         string           `json:"name"`
	Daemon       string           `json:"daemon"`
	Source       Source           `json:"source"`
	Capabilities dto.Capabilities `json:"capabilities"`
	Defaults     struct {
		Temperature float64 `json:"temperature"`
		MaxTokens   int     `json:"maxTokens"`
	} `json:"defaults"`
	ModeTags []string `json:"modeTags"`
}

// Typed manifest errors (interface.md §12.1 / failure-semantics §2).
var (
	ErrLanesConflict = errors.New("lanes-conflict: two models resolve to the same source on different daemons")
	ErrSchemaInvalid = errors.New("schema-invalid: manifest failed its JSON Schema")
)

// manifestSchema is the committed fleet-manifest JSON Schema (contracts/assets)
// embedded into the daemon binary. It lives in the module so the sole reader
// carries its own derived schema (ADR-0027) without a cross-repo copy at build time.
//
//go:embed fleet-manifest.schema.json
var manifestSchema []byte

// Load parses and validates a manifest file: structural via the JSON Schema,
// semantic (name uniqueness, port uniqueness, daemon references, lanes rule)
// here (ADR-0018 §4, data-model.md §2.4). The daemon is the sole caller.
func Load(path string) (*Manifest, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	return Parse(raw)
}

// Parse parses and validates manifest bytes (split from Load for testability).
func Parse(raw []byte) (*Manifest, error) {
	if err := validateSchema(raw); err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	if err := validateSemantic(&m); err != nil {
		return nil, err
	}
	return &m, nil
}

// validateSchema runs the committed draft-07 schema over the manifest (structure
// only; cross-entry identity checks are semantic — ADR-0018 §4).
func validateSchema(raw []byte) error {
	c := jsonschema.NewCompiler()
	c.Draft = jsonschema.Draft7
	if err := c.AddResource("fleet-manifest.schema.json", bytes.NewReader(manifestSchema)); err != nil {
		return err
	}
	schema, err := c.Compile("fleet-manifest.schema.json")
	if err != nil {
		return err
	}
	var v interface{}
	if err := json.Unmarshal(raw, &v); err != nil {
		return fmt.Errorf("%w: %v", ErrSchemaInvalid, err)
	}
	if err := schema.Validate(v); err != nil {
		return fmt.Errorf("%w: %v", ErrSchemaInvalid, err)
	}
	return nil
}

// validateSemantic enforces the cross-entry invariants the JSON Schema cannot
// express: unique daemon/model names, models[].daemon referencing an existing
// daemon, per-daemon port non-collision, and the lanes rule (ADR-0018 §4).
func validateSemantic(m *Manifest) error {
	daemonNames := map[string]bool{}
	ports := map[int]string{}
	daemons := map[string]Daemon{}
	for _, d := range m.Daemons {
		if daemonNames[d.Name] {
			return fmt.Errorf("duplicate daemon name %q", d.Name)
		}
		daemonNames[d.Name] = true
		if prev, ok := ports[d.Port]; ok {
			return fmt.Errorf("port %d collides between daemons %q and %q", d.Port, prev, d.Name)
		}
		ports[d.Port] = d.Name
		daemons[d.Name] = d
	}

	modelNames := map[string]bool{}
	// lanes: source identity (kind+repo+file) -> daemon name
	lanes := map[string]string{}
	for _, mo := range m.Models {
		if modelNames[mo.Name] {
			return fmt.Errorf("duplicate model name %q", mo.Name)
		}
		modelNames[mo.Name] = true
		if !daemonNames[mo.Daemon] {
			return fmt.Errorf("model %q references unknown daemon %q", mo.Name, mo.Daemon)
		}
		key := sourceKey(mo.Source)
		if prev, ok := lanes[key]; ok && prev != mo.Daemon {
			return fmt.Errorf("%w: %q and %q both resolve to source %q", ErrLanesConflict, mo.Name, prevModelFor(lanes, key, m), key)
		}
		lanes[key] = mo.Daemon
	}
	return nil
}

func sourceKey(s Source) string {
	switch s.Kind {
	case "hf":
		return "hf:" + s.Repo
	case "gguf":
		return "gguf:" + s.File
	default:
		return s.Kind + ":" + s.File + s.Repo
	}
}

// prevModelFor names the first model sharing a source key (for the lanes message).
func prevModelFor(lanes map[string]string, key string, m *Manifest) string {
	for _, mo := range m.Models {
		if sourceKey(mo.Source) == key && lanes[key] == mo.Daemon {
			return mo.Name
		}
	}
	return "?"
}

// listEntries projects the manifest into the daemon-http.md §2 `list` shape:
// one entry per model with its serving host/port, capabilities, defaults, and
// modeTags — exactly the projection internal/fleet parses.
func (m *Manifest) listEntries() []daemonEntry {
	out := make([]daemonEntry, 0, len(m.Models))
	daemons := map[string]Daemon{}
	for _, d := range m.Daemons {
		daemons[d.Name] = d
	}
	for _, mo := range m.Models {
		d := daemons[mo.Daemon]
		host := d.Host
		if host == "" {
			host = "127.0.0.1"
		}
		out = append(out, daemonEntry{
			Name:         mo.Name,
			Host:         host,
			Port:         d.Port,
			Capabilities: mo.Capabilities,
			Defaults: struct {
				Temperature float64 `json:"temperature"`
				MaxTokens   int     `json:"maxTokens"`
			}{Temperature: mo.Defaults.Temperature, MaxTokens: mo.Defaults.MaxTokens},
			ModeTags: mo.ModeTags,
		})
	}
	return out
}

// modelByName looks up one model; ok is false when absent (unknown-server).
func (m *Manifest) modelByName(name string) (Model, Daemon, bool) {
	var mo Model
	var found bool
	for _, x := range m.Models {
		if x.Name == name {
			mo = x
			found = true
			break
		}
	}
	if !found {
		return Model{}, Daemon{}, false
	}
	for _, d := range m.Daemons {
		if d.Name == mo.Daemon {
			return mo, d, true
		}
	}
	return Model{}, Daemon{}, false
}

// daemonEntry is the list verb's per-model projection (daemon-http.md §2). It is
// the exact wire shape internal/fleet's daemonEntry parses. The `state` field is
// daemon-owned live state merged into list output; it is unexported so it never
// leaks into the public DTO (ADR-0016 §1).
type daemonEntry struct {
	Name         string           `json:"name"`
	Host         string           `json:"host"`
	Port         int              `json:"port"`
	Capabilities dto.Capabilities `json:"capabilities"`
	Defaults     struct {
		Temperature float64 `json:"temperature"`
		MaxTokens   int     `json:"maxTokens"`
	} `json:"defaults"`
	ModeTags []string      `json:"modeTags"`
	state    dto.LiveState `json:"-"`
}
