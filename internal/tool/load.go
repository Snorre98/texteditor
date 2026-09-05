package tool

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/santhosh-tekuri/jsonschema/v5"

	"texteditor/config"
	"texteditor/shared/dto"
)

// ErrSchemaInvalid is surfaced when a tool file fails its JSON Schema (ADR-0019
// §2; mirrors the mode registry's schema-invalid).
var ErrSchemaInvalid = errors.New("schema-invalid: a tool file failed its JSON Schema")

// Load validates every embedded config/tools/*.json against the committed tool
// JSON Schema and registers them into a fresh Tool registry (ADR-0019 §1–§4). It
// returns the populated registry and the set of names, so the composition root can
// bind handlers and run the VerifyHandlers cross-check. Kept in the tool package
// (not the registries) so the root stays thin and the loader boundary-testable.
func Load() (Registry, []string, error) {
	files, err := config.Tools()
	if err != nil {
		return nil, nil, err
	}
	schema, err := loadToolSchema(config.ToolSchema)
	if err != nil {
		return nil, nil, err
	}

	reg := NewRegistry()
	var names []string
	for _, f := range files {
		def, err := parseAndValidateTool(schema, f)
		if err != nil {
			return nil, nil, err
		}
		if err := reg.Register(def); err != nil {
			return nil, nil, fmt.Errorf("register %s: %w", f.Name, err)
		}
		names = append(names, def.Name)
	}
	return reg, names, nil
}

func loadToolSchema(raw []byte) (*jsonschema.Schema, error) {
	c := jsonschema.NewCompiler()
	c.Draft = jsonschema.Draft7
	if err := c.AddResource("tool.schema.json", bytes.NewReader(raw)); err != nil {
		return nil, err
	}
	return c.Compile("tool.schema.json")
}

// parseAndValidateTool unmarshals one tool file and validates it against the
// schema, stripping the instance "$schema" meta-pointer (see mode registry).
func parseAndValidateTool(schema *jsonschema.Schema, f config.ToolFile) (dto.ToolDef, error) {
	data := f.Data

	var v interface{}
	if err := json.Unmarshal(data, &v); err != nil {
		return dto.ToolDef{}, fmt.Errorf("%w (%s): %v", ErrSchemaInvalid, f.Name, err)
	}

	var stripped map[string]json.RawMessage
	if err := json.Unmarshal(data, &stripped); err == nil {
		delete(stripped, "$schema")
		if b, err := json.Marshal(stripped); err == nil {
			data = b
			var vv interface{}
			if json.Unmarshal(data, &vv) == nil {
				v = vv
			}
		}
	}

	if err := schema.Validate(v); err != nil {
		return dto.ToolDef{}, fmt.Errorf("%w (%s): %v", ErrSchemaInvalid, f.Name, err)
	}

	var raw struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return dto.ToolDef{}, fmt.Errorf("%w (%s): %v", ErrSchemaInvalid, f.Name, err)
	}

	return dto.ToolDef{
		Name:        raw.Name,
		Description: raw.Description,
		Parameters:  raw.Parameters,
	}, nil
}
