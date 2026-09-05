// Package config embeds the versioned mode/tool data files and their JSON
// Schemas (ADR-0019 §1). The registries load-and-validate these at startup; the
// data lives in config/modes/*.json and config/tools/*.json, with schemas in
// config/schemas/*.json, all go:embed'd into the static binary (ADR-0003).
package config

import (
	"embed"
	"io/fs"
	"path/filepath"
	"strings"
)

// ModeFile is one mode definition plus its source filename (for error messages).
type ModeFile struct {
	Name string // basename, e.g. "proofreader.json"
	Data []byte
}

// ToolFile is one tool definition plus its source filename.
type ToolFile struct {
	Name string // basename, e.g. "retrieve.json"
	Data []byte
}

//go:embed modes/*.json tools/*.json schemas/*.json
var embedded embed.FS

// ModeSchema is the committed mode JSON Schema.
//
//go:embed schemas/mode.schema.json
var ModeSchema []byte

// ToolSchema is the committed tool JSON Schema.
//
//go:embed schemas/tool.schema.json
var ToolSchema []byte

// Modes returns the embedded mode definitions, sorted by filename.
func Modes() ([]ModeFile, error) {
	entries, err := fs.ReadDir(embedded, "modes")
	if err != nil {
		return nil, err
	}
	var out []ModeFile
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := embedded.ReadFile(filepath.Join("modes", e.Name()))
		if err != nil {
			return nil, err
		}
		out = append(out, ModeFile{Name: e.Name(), Data: b})
	}
	return out, nil
}

// Tools returns the embedded tool definitions, sorted by filename.
func Tools() ([]ToolFile, error) {
	entries, err := fs.ReadDir(embedded, "tools")
	if err != nil {
		return nil, err
	}
	var out []ToolFile
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := embedded.ReadFile(filepath.Join("tools", e.Name()))
		if err != nil {
			return nil, err
		}
		out = append(out, ToolFile{Name: e.Name(), Data: b})
	}
	return out, nil
}
