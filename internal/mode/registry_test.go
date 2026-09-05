package mode

import (
	"encoding/json"
	"errors"
	"testing"

	"texteditor/config"
)

// validInput matches the embedded config data (4 modes, 4 models, 4 tools).
func validInput() ValidationInput {
	return ValidationInput{
		Models: []string{"gemma4-12b", "gemma4-26b", "mistral-24b", "llama3.1-8b"},
		ModelTags: map[string][]string{
			"gemma4-12b":    {"proofreader"},
			"gemma4-26b":    {"editor"},
			"mistral-24b":   {"drafter"},
			"llama3.1-8b":   {"grammar"},
		},
		Tools: []string{"edit_markdown", "retrieve", "read_note", "diff"},
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
	// toolCalling defaults to native (ADR-0028 §3; ADR-0019 §4).
	for _, m := range list {
		if m.ToolCalling != "native" {
			t.Fatalf("mode %s toolCalling = %q, want native", m.Name, m.ToolCalling)
		}
	}
	// Get returns a mode.
	m, err := r.Get("proofreader")
	if err != nil {
		t.Fatal(err)
	}
	if m.DefaultModel != "gemma4-12b" {
		t.Fatalf("defaultModel = %q", m.DefaultModel)
	}
	if len(m.ToolAllowlist) == 0 {
		t.Fatal("allowlist empty")
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

func TestUnknownToolFails(t *testing.T) {
	in := validInput()
	in.Tools = []string{"edit_markdown"} // drop retrieve/read_note/diff
	_, err := New(in)
	if !errors.Is(err, ErrUnknownTool) {
		t.Fatalf("want ErrUnknownTool, got %v", err)
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
