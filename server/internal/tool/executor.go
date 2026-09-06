package tool

import (
	"encoding/json"
)

// Handler is a tool's Go implementation: name-keyed into the executor's private
// map (ADR-0019 §3). The name is the whole seam; no reflection, no data naming a
// Go symbol.
type Handler func(args json.RawMessage) (json.RawMessage, error)

// ExecutorImpl is the concrete Tool executor. It satisfies the Executor
// interface; Bind is only reachable at the composition root (startup), where
// handlers are bound before the executor is handed to the loop.
type ExecutorImpl struct {
	handlers map[string]Handler
}

// NewExecutor returns an empty ExecutorImpl.
func NewExecutor() *ExecutorImpl {
	return &ExecutorImpl{handlers: map[string]Handler{}}
}

// Bind maps a tool name to its handler in the executor's private map. The name
// must match a registered ToolDef (verified at startup, ADR-0019 §3).
func (e *ExecutorImpl) Bind(name string, h Handler) {
	e.handlers[name] = h
}

// Invoke dispatches name to its bound handler (interface.md §8).
func (e *ExecutorImpl) Invoke(name string, args json.RawMessage) (json.RawMessage, error) {
	h, ok := e.handlers[name]
	if !ok {
		return nil, ErrToolNoHandler
	}
	return h(args)
}

// HandlerNames returns the set of bound handler names (the startup cross-check:
// every registered name must have a handler — ADR-0019 §3).
func (e *ExecutorImpl) HandlerNames() map[string]bool {
	out := make(map[string]bool, len(e.handlers))
	for name := range e.handlers {
		out[name] = true
	}
	return out
}
