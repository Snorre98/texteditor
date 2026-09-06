package tool

import (
	"errors"
	"fmt"
	"sort"
)

// ErrMissingHandler is the startup cross-check failure (ADR-0019 §2/§3): a
// registered tool definition has no bound Go handler. Distinct from
// ErrToolNoHandler, which is the runtime Invoke error on an unbound name.
var ErrMissingHandler = errors.New("tool-has-no-handler: a registered tool has no bound handler")

// MissingHandlers returns the set of registered tool names with no bound
// executor handler (the startup cross-check, ADR-0019 §3). It is pure glue over
// Registry.List and Executor handler names; the composition root runs it and
// fails startup with tool-has-no-handler naming the missing set.
func MissingHandlers(reg Registry, handlers map[string]bool) []string {
	var missing []string
	for _, t := range reg.List() {
		if !handlers[t.Name] {
			missing = append(missing, t.Name)
		}
	}
	sort.Strings(missing)
	return missing
}

// VerifyHandlers runs the startup cross-check and returns a typed error naming
// the missing names (or nil when every registered tool has a handler).
func VerifyHandlers(reg Registry, handlers map[string]bool) error {
	missing := MissingHandlers(reg, handlers)
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %v", ErrMissingHandler, missing)
}
