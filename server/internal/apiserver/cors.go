// CORS policy for the webview/web targets (ADR-0037): an explicit origin
// allowlist, opt-in, no wildcard. Empty (the default) = CORS disabled, so the
// standalone-daemon and TUI paths are bit-for-bit unchanged.
//
// Implementation note (recorded judgment call): ADR-0037 §2 phrases this as "one
// ogen `WithMiddleware` global middleware", but ogen's middleware model is
// `func(Request, Next) (Response, error)` — it is invoked *inside* the operation
// after request decode and has no `http.ResponseWriter` access, so it cannot set
// response headers or short-circuit an OPTIONS preflight. The policy is therefore
// a plain `net/http` middleware applied in `Server.ServeHTTP` around the generated
// ogen router. It satisfies every functional requirement of ADR-0037 §2: the
// header is set on every response (including the `/turn` raw SSE stream, because
// it runs before the ogen router hands the writer to `StartTurn`, and including
// the OPTIONS preflight), and OPTIONS is short-circuited with 204. With an empty
// allowlist no middleware is installed and the behavior is unchanged.
package apiserver

import (
	"net/http"
	"strings"
)

// corsPolicy is an explicit origin allowlist. A nil policy disables CORS entirely.
type corsPolicy struct {
	allowed map[string]struct{}
}

// newCORSPolicy builds a policy from the allowlist, trimming whitespace and
// rejecting empty/`*` entries. A policy with no allowed origins is nil (disabled).
func newCORSPolicy(origins []string) *corsPolicy {
	m := make(map[string]struct{}, len(origins))
	for _, o := range origins {
		o = strings.TrimSpace(o)
		if o == "" || o == "*" {
			continue
		}
		m[o] = struct{}{}
	}
	if len(m) == 0 {
		return nil
	}
	return &corsPolicy{allowed: m}
}

// apply sets CORS headers on every response (ADR-0037 §2) and short-circuits an
// OPTIONS preflight. It returns true when the request was fully handled (a
// preflight); the caller must then stop.
func (p *corsPolicy) apply(w http.ResponseWriter, r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin != "" {
		// Vary on the origin even when it is not allowed: the response differs by
		// origin, so a shared cache must not reuse an allowed response for a
		// disallowed one.
		w.Header().Add("Vary", "Origin")
		if _, ok := p.allowed[origin]; ok {
			w.Header().Set("Access-Control-Allow-Origin", origin)
		}
	}

	if r.Method != http.MethodOptions {
		return false
	}
	// Preflight short-circuit (ADR-0037 §2). `POST /turn` carries
	// `Content-Type: application/json` + `Accept: text/event-stream` — both
	// CORS-non-safelisted — so the browser preflights. Answer only allowed
	// origins; a disallowed origin still gets 204 with no allow headers, which
	// the browser blocks (fail closed).
	if origin != "" {
		if _, ok := p.allowed[origin]; ok {
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "content-type, accept")
		}
	}
	w.WriteHeader(http.StatusNoContent)
	return true
}
