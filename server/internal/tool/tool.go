// Package tool holds the Tool registry (definitions — a pure data leaf) and
// the Tool executor (execution — name-keyed handler map), split per ADR-0016 §5.
package tool

import (
	"encoding/json"

	"texteditor/shared/dto"
)

// Registry is the Tool registry public API (interface.md §8). Since ADR-0045
// tools are global: the loop advertises List() on every turn; there is no
// per-mode allowlist.
type Registry interface {
	Register(tool dto.ToolDef) error
	List() []dto.ToolDef
}

// Executor is the Tool executor public API (interface.md §8).
type Executor interface {
	Invoke(name string, args json.RawMessage) (json.RawMessage, error)
}
