// Package locate is the sealed `/locate` chunk-anchoring resolver (ADR-0048).
// It is deterministic and token-free: normalize the pasted chunk
// (markdown-stripped, whitespace-collapsed, case-folded), match exact normalized
// equality first (with a per-block hash fast path), then fall back to a
// conservative fuzzy (trigram/token overlap) score. It never calls a model and
// never touches the embedding path.
package locate

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

// Normalize strips markdown structural markers, collapses whitespace, and
// lowercases the text, so whitespace- and markdown-only differences compare
// equal (ADR-0048 §2). The same function is applied to the pasted chunk and to
// every candidate block/chunk.
func Normalize(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if fenceMarkerRe.MatchString(line) {
			continue // fence delimiters are structure, not content
		}
		line = headingMarkerRe.ReplaceAllString(line, "")
		line = blockquoteMarkerRe.ReplaceAllString(line, "")
		line = listMarkerRe.ReplaceAllString(line, "")
		line = linkRe.ReplaceAllString(line, "$1")
		line = emphasisRe.ReplaceAllString(line, "")
		out = append(out, line)
	}
	joined := strings.Join(out, " ")
	joined = whitespaceRe.ReplaceAllString(joined, " ")
	return strings.ToLower(strings.TrimSpace(joined))
}

// NormalizedHash is the short content hash of a normalized string; it is the
// exact-match fast path (ADR-0048 §2). Normalize(a) == Normalize(b) iff their
// hashes are equal (modulo collision).
func NormalizedHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:8])
}

var (
	headingMarkerRe    = regexp.MustCompile(`^\s{0,3}#{1,6}\s+`)
	blockquoteMarkerRe = regexp.MustCompile(`^\s{0,3}>\s?`)
	listMarkerRe       = regexp.MustCompile(`^\s{0,3}(?:[-*+]|\d+[.)])\s+`)
	fenceMarkerRe      = regexp.MustCompile("^\\s{0,3}(```|~~~)")
	linkRe             = regexp.MustCompile(`!?\[([^\]]*)\]\([^)]*\)`)
	emphasisRe         = regexp.MustCompile("[*_~`]+")
	whitespaceRe       = regexp.MustCompile(`\s+`)
)
