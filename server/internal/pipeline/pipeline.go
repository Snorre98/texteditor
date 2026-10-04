// Package pipeline holds the one global turn pipeline policy (ADR-0045 §3): a
// sealed data leaf that loads the go:embed'd config/pipeline.json, validates it
// against the committed JSON Schema, and fail-fasts at startup with a typed
// `pipeline-invalid` error. Every preset runs the same policy.
package pipeline

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/santhosh-tekuri/jsonschema/v5"

	"texteditor/config"
	"texteditor/shared/dto"
)

// Interface is the Pipeline policy public API (interface.md §8c).
type Interface interface {
	Policy() dto.PipelinePolicy
}

// ErrInvalid is the typed startup error for a broken policy file (ADR-0045 §3,
// failure-semantics §2).
var ErrInvalid = errors.New("pipeline-invalid: config/pipeline.json failed its JSON Schema")

// registry is the concrete policy holder (a pure leaf, no out-edges).
type registry struct {
	policy dto.PipelinePolicy
}

// New loads and validates the embedded pipeline policy, returning a ready
// registry or a typed ErrInvalid.
func New() (Interface, error) {
	p, err := parse(config.PipelineSchema, config.Pipeline)
	if err != nil {
		return nil, err
	}
	return &registry{policy: p}, nil
}

// Policy returns the validated, immutable policy.
func (r *registry) Policy() dto.PipelinePolicy { return r.policy }

// parse compiles the schema, validates the data, and decodes it. Unexported so
// tests can exercise rejection paths against the committed schema.
func parse(schemaBytes, data []byte) (dto.PipelinePolicy, error) {
	c := jsonschema.NewCompiler()
	c.Draft = jsonschema.Draft7 // match the committed schema ($schema draft-07)
	if err := c.AddResource("pipeline.schema.json", bytes.NewReader(schemaBytes)); err != nil {
		return dto.PipelinePolicy{}, fmt.Errorf("%w: schema compile: %v", ErrInvalid, err)
	}
	schema, err := c.Compile("pipeline.schema.json")
	if err != nil {
		return dto.PipelinePolicy{}, fmt.Errorf("%w: schema compile: %v", ErrInvalid, err)
	}

	var v interface{}
	if err := json.Unmarshal(data, &v); err != nil {
		return dto.PipelinePolicy{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if err := schema.Validate(v); err != nil {
		return dto.PipelinePolicy{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}

	var raw struct {
		MaxSteps         int `json:"maxSteps"`
		MaxHistoryTokens int `json:"maxHistoryTokens"`
		MaxRagTokens     int `json:"maxRagTokens"`
		MaxMentionTokens int `json:"maxMentionTokens"`
		AutoRagTopK      int `json:"autoRagTopK"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return dto.PipelinePolicy{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return dto.PipelinePolicy{
		MaxSteps:         raw.MaxSteps,
		MaxHistoryTokens: raw.MaxHistoryTokens,
		MaxRagTokens:     raw.MaxRagTokens,
		MaxMentionTokens: raw.MaxMentionTokens,
		AutoRagTopK:      raw.AutoRagTopK,
	}, nil
}
