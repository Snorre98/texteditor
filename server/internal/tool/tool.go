// Package tool holds the Tool registry (definitions — a pure data leaf) and
// the Tool executor (execution — name-keyed handler map), split per ADR-0016 §5.
package tool

import (
	"context"
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

// Executor is the Tool executor public API (interface.md §8). The context
// carries the turn's workspace-scoped services (the loop injects the shard
// lease) so document-scoped tools like retrieve/read_note reach the right
// workspace index (ADR-0049 §5).
type Executor interface {
	Invoke(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error)
}
