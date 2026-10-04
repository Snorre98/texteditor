package mode

import (
	"encoding/json"
	"errors"
	"testing"

	"texteditor/config"
)

// validInput matches the embedded config data (4 presets, 3 models).
func validInput() ValidationInput {
	return ValidationInput{
		Models: []string{"gemma4-26b-moe", "mistral-24b", "llama3.1-8b"},
		ModelTags: map[string][]string{
			"gemma4-26b-moe": {"editor", "proofreader"},
			"mistral-24b":    {"drafter"},
			"llama3.1-8b":    {"grammar"},
		},
	}
}

func TestNewLoadsAllModes(t *testing.T) {
	r, err := New(validInput())
	if err != nil {
		t.Fatal(err)
	}
	list := r.List()
	if len(list) != 4 {
		t.Fatalf("modes = %d, want 4", len(list))
	}
	// ADR-0045: a preset is exactly name + systemPrompt + defaultModel.
	for _, m := range list {
		if m.Name == "" || m.SystemPrompt == "" || m.DefaultModel == "" {
			t.Fatalf("preset %+v has an empty required field", m)
		}
	}
	// Get returns a mode.
	m, err := r.Get("proofreader")
	if err != nil {
		t.Fatal(err)
	}
	if m.DefaultModel != "gemma4-26b-moe" {
		t.Fatalf("defaultModel = %q", m.DefaultModel)
	}
}

func TestGetNotFound(t *testing.T) {
	r, err := New(validInput())
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.Get("nope")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestUnknownModelFails(t *testing.T) {
	in := validInput()
	in.Models = []string{"only-this-model"}
	_, err := New(in)
	if !errors.Is(err, ErrUnknownModel) {
		t.Fatalf("want ErrUnknownModel, got %v", err)
	}
}

func TestUnreachableNoTagFails(t *testing.T) {
	in := validInput()
	in.ModelTags = map[string][]string{} // no mode advertised anywhere
	_, err := New(in)
	if !errors.Is(err, ErrUnreachableNoTag) {
		t.Fatalf("want ErrUnreachableNoTag, got %v", err)
	}
}

func TestSchemaInvalid(t *testing.T) {
	// Compile the committed schema and reject a mode missing required fields.
	s, err := loadSchema(config.ModeSchema)
	if err != nil {
		t.Fatal(err)
	}
	var v interface{}
	if err := json.Unmarshal([]byte(`{"name":"x"}`), &v); err != nil {
		t.Fatal(err)
	}
	if err := s.Validate(v); err == nil {
		t.Fatal("expected schema validation to fail for a mode missing systemPrompt/defaultModel")
	}
}

// TestBehavioralFieldsRejected is the ADR-0045 gate: removed behavioral fields
// (agentic, maxSteps, toolAllowlist, toolCalling, contextBudget, params,
// preamble, kind) are not part of the schema and fail validation.
func TestBehavioralFieldsRejected(t *testing.T) {
	s, err := loadSchema(config.ModeSchema)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{
		`"toolAllowlist":["edit_markdown"]`,
		`"params":{"temperature":0.3}`,
		`"contextBudget":{"maxRagTokens":1}`,
		`"maxSteps":4`,
		`"agentic":true`,
		`"kind":"model"`,
		`"preamble":"x"`,
		`"toolCalling":"native"`,
	} {
		raw := `{"name":"x","systemPrompt":"p","defaultModel":"m",` + field + `}`
		var v interface{}
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			t.Fatal(err)
		}
		if err := s.Validate(v); err == nil {
			t.Fatalf("expected schema to reject behavioral field %s", field)
		}
	}
}
