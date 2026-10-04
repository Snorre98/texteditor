// Package pathutil is a pure leaf holding the engine's canonical-path
// discipline (ADR-0047 §2, ADR-0049 §2/§3): symlink-resolved absolute paths,
// case-folded identity keys, stable path-derived ids, and boundary checks.
//
// Document identity (internal/document), workspace roots (internal/workspace),
// the Filesystem leaf, and the corpus service all canonicalize through here so
// aliases and symlinks resolve to one entity everywhere.
package pathutil

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
)

// Canonical resolves a path to its canonical absolute form and its case-folded
// identity key. Symlinks are evaluated; for a path that does not exist yet, the
// deepest existing ancestor is resolved and the remainder re-appended (so a
// not-yet-created file under a symlinked root still canonicalizes consistently,
// e.g. macOS /var → /private/var). The key lowercases the canonical path so
// case variants of one file resolve to one entity (ADR-0047 §2).
func Canonical(path string) (canonical, key string) {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = filepath.Clean(path)
	}
	canonical = resolveExisting(filepath.Clean(abs))
	return canonical, strings.ToLower(canonical)
}

// resolveExisting resolves symlinks in the deepest existing prefix of an
// absolute path and re-appends the non-existing remainder.
func resolveExisting(abs string) string {
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return filepath.Clean(resolved)
	}
	dir, base := filepath.Split(abs)
	dir = filepath.Clean(dir)
	if dir == abs { // reached the filesystem root without resolving
		return abs
	}
	return filepath.Join(resolveExisting(dir), base)
}

// Within reports whether path lies inside root (or equals it), comparing
// case-folded canonical forms so case-insensitive filesystems behave (ADR-0047
// §2). Both arguments are expected to be canonical absolute paths; callers
// canonicalize first.
func Within(path, root string) bool {
	p := strings.ToLower(filepath.Clean(path))
	r := strings.ToLower(filepath.Clean(root))
	if p == r {
		return true
	}
	return strings.HasPrefix(p, r+string(filepath.Separator))
}

// Hash returns the hex-encoded SHA-256 of s. It is the content-hash discipline
// shared by the document store, the retriever, and the corpus service.
func Hash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// DocID returns a stable, URL-safe corpus document id for a canonical path:
// the first 32 hex characters of SHA-256(canonical path). Corpus files have no
// surrogate document id (ADR-0049 §4); this id addresses one path-keyed corpus
// file across the API.
func DocID(canonicalPath string) string {
	return Hash(canonicalPath)[:32]
}
