// Package mode holds the Mode registry — a pure data leaf (ADR-0019, ADR-0016
// §4). It loads the go:embed'd config/modes/*.json, validates them against the
// committed mode JSON Schema, and fail-fasts at startup on broken references.
//
// Since ADR-0045 a mode is a prompt preset: name + systemPrompt + defaultModel.
// The registry stays a leaf by accepting the external facts it needs (the fleet's
// model names + their modeTags) at construction, rather than reaching into Fleet
// (ADR-0019 §2's validation). The router gates run at the composition root — see
// implementation-sequence.md "Sequencing note".
package mode

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/santhosh-tekuri/jsonschema/v5"

	"texteditor/config"
	"texteditor/shared/dto"
)

// ModeRegistry is the Mode registry public API (interface.md §8).
type ModeRegistry interface {
	List() []Mode
	Get(name string) (Mode, error)
}

// Interface is an alias for ModeRegistry (the contracted name, interface.md §8).
type Interface = ModeRegistry

// Mode is the resolved, validated mode (the pure DTO, ADR-0045).
type Mode = dto.Mode

// ValidationInput supplies the external facts the registry cross-checks against,
// keeping it a pure leaf (ADR-0019 §2). ModelTags maps a model name to the
// modes (modeTags) that select it, for the reachability gate.
type ValidationInput struct {
	Models    []string            // fleet manifest model names
	ModelTags map[string][]string // model name -> modeTags advertising it
}

// Typed validation errors (ADR-0019 §2).
var (
	ErrSchemaInvalid    = errors.New("schema-invalid: a mode file failed its JSON Schema")
	ErrUnknownModel     = errors.New("mode-refs-unknown-model: defaultModel not in the fleet manifest")
	ErrUnreachableNoTag = errors.New("mode-unreachable-no-tag: mode name appears in no model's modeTags")
	ErrNotFound         = errors.New("mode not found")
)

// ValidationError wraps one validation failure with context.
type ValidationError struct {
	Err  error
	File string
	Mode string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("%v (file %s, mode %s)", e.Err, e.File, e.Mode)
}
func (e *ValidationError) Unwrap() error { return e.Err }

// registry is the concrete Mode registry (a leaf, no out-edges).
type registry struct {
	modes map[string]Mode
	order []string
}

// New loads and validates every embedded mode file (ADR-0019 §2), returning a
// ready registry or a typed *ValidationError.
func New(in ValidationInput) (ModeRegistry, error) {
	files, err := config.Modes()
	if err != nil {
		return nil, err
	}
	schema, err := loadSchema(config.ModeSchema)
	if err != nil {
		return nil, err
	}

	// Build lookup sets.
	models := map[string]bool{}
	for _, m := range in.Models {
		models[m] = true
	}
	// modesByTag[model] = set of modes that select it (fleet policy, ADR-0015).
	modesByTag := map[string]bool{}
	for _, tags := range in.ModelTags {
		for _, tag := range tags {
			modesByTag[tag] = true
		}
	}
	reachable := modesByTag // a mode is reachable if some model's modeTags name it

	reg := &registry{modes: map[string]Mode{}, order: []string{}}
	for _, f := range files {
		m, err := parseAndValidate(schema, f)
		if err != nil {
			return nil, err
		}

		// defaultModel must resolve via the manifest.
		if !models[m.DefaultModel] {
			return nil, &ValidationError{Err: ErrUnknownModel, File: f.Name, Mode: m.Name}
		}
		// The mode's name must appear in at least one model's modeTags.
		if !reachable[m.Name] {
			return nil, &ValidationError{Err: ErrUnreachableNoTag, File: f.Name, Mode: m.Name}
		}

		reg.modes[m.Name] = m
		reg.order = append(reg.order, m.Name)
	}
	sort.Strings(reg.order)
	return reg, nil
}

// List returns modes in name order.
func (r *registry) List() []Mode {
	out := make([]Mode, 0, len(r.order))
	for _, name := range r.order {
		out = append(out, r.modes[name])
	}
	return out
}

// Get returns one mode by name.
func (r *registry) Get(name string) (Mode, error) {
	m, ok := r.modes[name]
	if !ok {
		return Mode{}, ErrNotFound
	}
	return m, nil
}

// loadSchema compiles a JSON Schema once.
func loadSchema(raw []byte) (*jsonschema.Schema, error) {
	c := jsonschema.NewCompiler()
	c.Draft = jsonschema.Draft7 // match the committed schemas ($schema draft-07)
	if err := c.AddResource("mode.schema.json", bytes.NewReader(raw)); err != nil {
		return nil, err
	}
	return c.Compile("mode.schema.json")
}

// stripSchemaKeyword removes the instance-level "$schema" meta-pointer, which is
// valid to carry in the data files (it names the schema) but is treated as an
// additional property by draft-07 validators under `additionalProperties: false`.
func stripSchemaKeyword(data []byte) []byte {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return data
	}
	delete(m, "$schema")
	out, err := json.Marshal(m)
	if err != nil {
		return data
	}
	return out
}

// parseAndValidate unmarshals one mode file and validates it against the schema.
// ADR-0045 removed the behavioral fields (and their defaults): the mode is
// exactly name + systemPrompt + defaultModel.
func parseAndValidate(schema *jsonschema.Schema, f config.ModeFile) (Mode, error) {
	data := stripSchemaKeyword(f.Data)

	var v interface{}
	if err := json.Unmarshal(data, &v); err != nil {
		return Mode{}, &ValidationError{Err: ErrSchemaInvalid, File: f.Name, Mode: "(unknown)"}
	}
	if err := schema.Validate(v); err != nil {
		return Mode{}, &ValidationError{Err: fmt.Errorf("%w: %v", ErrSchemaInvalid, err), File: f.Name, Mode: "(unknown)"}
	}

	var raw struct {
		Name         string `json:"name"`
		SystemPrompt string `json:"systemPrompt"`
		DefaultModel string `json:"defaultModel"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return Mode{}, &ValidationError{Err: ErrSchemaInvalid, File: f.Name, Mode: raw.Name}
	}
	return Mode{
		Name:         raw.Name,
		SystemPrompt: raw.SystemPrompt,
		DefaultModel: raw.DefaultModel,
	}, nil
}
