package tool

import (
	"errors"
	"sort"

	"texteditor/shared/dto"
)

// ReservedRequestToolName is the synthetic request_tool name that must never be a
// registered tool (ADR-0028 §2). The registry rejects any real tool by this name.
const ReservedRequestToolName = "request_tool"

var (
	ErrDuplicate       = errors.New("tool already registered")
	ErrReservedName    = errors.New("tool name is reserved (request_tool)")
	ErrUnregistered    = errors.New("tool is not registered")
	ErrToolNoHandler   = errors.New("tool has no handler")
	ErrUnknownTool     = errors.New("unknown tool")
)

type registry struct {
	defs map[string]dto.ToolDef
}

// NewRegistry returns an empty Tool registry.
func NewRegistry() Registry {
	return &registry{defs: map[string]dto.ToolDef{}}
}

// Register adds a tool definition (ADR-0019 §3). It rejects duplicates and the
// reserved request_tool name (ADR-0028 §2).
func (r *registry) Register(t dto.ToolDef) error {
	if t.Name == ReservedRequestToolName {
		return ErrReservedName
	}
	if _, ok := r.defs[t.Name]; ok {
		return ErrDuplicate
	}
	r.defs[t.Name] = t
	return nil
}

// List returns registered tool definitions in a stable (name-sorted) order.
func (r *registry) List() []dto.ToolDef {
	out := make([]dto.ToolDef, 0, len(r.defs))
	for _, d := range r.defs {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// AllowlistFor returns the definitions in mode.ToolAllowlist, preserving the
// allowlist order (the order spliced into the payload).
func (r *registry) AllowlistFor(mode dto.Mode) []dto.ToolDef {
	var out []dto.ToolDef
	for _, name := range mode.ToolAllowlist {
		if d, ok := r.defs[name]; ok {
			out = append(out, d)
		}
	}
	return out
}
