// Package routergate holds the two ADR-0028 §4 fail-fast startup gates for the
// optional router seam. It is a composition-root helper leaf, NOT part of the
// Mode registry (sequencing note, implementation-sequence.md): the gates need
// Fleet facts (the needle-router model's presence and manifest fingerprint), so
// they run where Fleet is already wired. `Check` is a no-op when no mode opts
// into the router.
package routergate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"texteditor/shared/dto"
)

// RouterModelName is the router model the gates check (ADR-0028 §7).
const RouterModelName = "needle-router"

// Typed startup errors (ADR-0028 §4, failure-semantics §2).
var (
	ErrRouterUnavailable = errors.New("mode-refs-router-unavailable: a mode has toolCalling \"router\" but no resolvable needle-router model in the fleet manifest")
	ErrToolsStale        = errors.New("router-tools-stale: the manifest's needle-router fingerprint differs from the engine's tool-set hash")
)

// ToolSetHash is the deterministic engine-side fingerprint over the canonical
// sorted set of (name, description, parameters) of the tool registry
// (ADR-0028 §4). The algorithm is documented so D1's `needle finetune`
// (macos-dev-config) records the same value into the manifest:
//
//	sha256 over, per tool sorted by name: name NUL description NUL
//	canonical-parameters NUL, in that order, hex-encoded, prefixed "sha256:".
//
// "canonical-parameters" is the JSON Schema compacted by round-tripping it
// through encoding/json (whitespace- and key-order-insensitive).
func ToolSetHash(defs []dto.ToolDef) string {
	rows := make([][3]string, 0, len(defs))
	for _, d := range defs {
		rows = append(rows, [3]string{d.Name, d.Description, canonicalJSON(d.Parameters)})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i][0] < rows[j][0] })

	h := sha256.New()
	for _, r := range rows {
		for _, s := range r {
			h.Write([]byte(s))
			h.Write([]byte{0})
		}
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// Check runs the two startup gates at the composition root (ADR-0028 §4,
// ADR-0019 §2 discipline). It returns nil when no mode has
// toolCalling: "router", ErrRouterUnavailable when one does but no resolvable
// needle-router model is in the manifest projection, and ErrToolsStale when the
// manifest fingerprint (empty included) differs from the engine's tool-set
// hash. `fingerprint` returns the daemon list projection's fingerprint for a
// model ("" when absent) — the Fleet gateway's Fingerprint op.
func Check(modes []dto.Mode, modelPresent func(name string) bool, fingerprint func(name string) (string, error), toolHash string) error {
	if !wantsRouter(modes) {
		return nil
	}
	if !modelPresent(RouterModelName) {
		return ErrRouterUnavailable
	}
	fp, err := fingerprint(RouterModelName)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrRouterUnavailable, err)
	}
	if fp != toolHash {
		return ErrToolsStale
	}
	return nil
}

func wantsRouter(modes []dto.Mode) bool {
	for _, m := range modes {
		if m.ToolCalling == "router" {
			return true
		}
	}
	return false
}

// canonicalJSON compacts raw JSON through a round-trip (whitespace- and
// key-order-insensitive). Invalid JSON falls back to the raw bytes — the Tool
// registry validates schemas at load, so this is a defensive path only.
func canonicalJSON(raw []byte) string {
	var v interface{}
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	out, err := json.Marshal(v)
	if err != nil {
		return string(raw)
	}
	return string(out)
}
