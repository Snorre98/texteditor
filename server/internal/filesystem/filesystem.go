// Package filesystem holds the Filesystem module — read-only filesystem reach
// bounded by an allowlist (ADR-0035, renamed from Workspace by ADR-0049 §5; the
// ALLOWED_ROOTS boundary is ADR-0049 §6). It owns a shallow, non-recursive
// directory listing and bounded raw file reads, keeping stateless filesystem
// access distinct from the versioning Document store. A pure leaf: no state, no
// database, no cache; its hidden internals are os.ReadDir/os.ReadFile plus path
// validation.
//
// Every path is canonicalized (symlinks resolved, case-folded identity,
// ADR-0047 §2) and must lie inside one of the configured allowed roots. A path
// outside the boundary is refused with the typed ErrPathOutsideAllowedRoots —
// never silently ignored — so a LAN-bound engine (ENGINE_BIND=0.0.0.0) cannot
// browse or read the whole filesystem (ADR-0021, ADR-0049 §6).
package filesystem

import (
	"context"
	"errors"
	"io"
	iofs "io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"texteditor/internal/pathutil"
)

// Entry is one directory entry (interface.md §9b, ADR-0035 §1).
type Entry struct {
	Name  string // bare file/dir name
	Path  string // canonical absolute path
	IsDir bool
}

// Filesystem is the Filesystem public API (interface.md §9b, renamed by
// ADR-0049 §5).
type Filesystem interface {
	// List returns the direct, non-recursive children of dir, sorted by name
	// (case-insensitive). Hidden entries (dotfiles) are returned; filtering for
	// display is client-side presentation (ADR-0035 §1). dir must lie inside an
	// allowed root.
	List(ctx context.Context, dir string) ([]Entry, error)
	// Read returns at most maxBytes of raw file content. It never registers,
	// versions, or indexes anything — mentioned-file context reads through here
	// and is provably side-effect-free (ADR-0036 §6). path must lie inside an
	// allowed root.
	Read(ctx context.Context, path string, maxBytes int) ([]byte, error)
	// AllowedRoots returns the canonical allowlist (defense-in-depth
	// introspection for the API's typed refusal payload).
	AllowedRoots() []string
	// Check validates that path lies inside an allowed root without requiring
	// it to exist or be a regular file. Corpus scope validation uses it so a
	// file root and a directory root are both bounded (ADR-0049 §6).
	Check(path string) error
}

// Interface is an alias for Filesystem (the contracted name, interface.md §9b).
type Interface = Filesystem

// Typed errors (interface.md §9b, ADR-0035 §1, ADR-0049 §6). The loop maps
// these to the mention SSE codes; the API server maps List failures to
// not-found / not-a-directory / path-outside-allowed-roots.
var (
	ErrNotFound                = errors.New("not-found")
	ErrNotADirectory           = errors.New("not-a-directory")
	ErrNotRegular              = errors.New("not-regular")
	ErrTooLarge                = errors.New("too-large")
	ErrReadFailed              = errors.New("read-failed")
	ErrPathOutsideAllowedRoots = errors.New("path-outside-allowed-roots: the path is outside the ALLOWED_ROOTS allowlist")
	ErrNoAllowedRoots          = errors.New("no-allowed-roots: ALLOWED_ROOTS is empty")
)

// fs is the concrete Filesystem (a pure leaf, no out-edges).
type rooted struct {
	allowed []string // canonical absolute directories
}

// New returns a Filesystem bounded by allowedRoots. Each root is canonicalized
// and must be an existing directory; an empty allowlist defaults to $HOME
// (ADR-0049 §6). Construction is fail-fast: a broken allowlist is a startup
// error, not a runtime surprise.
func New(allowedRoots []string) (Filesystem, error) {
	if len(allowedRoots) == 0 {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, ErrNoAllowedRoots
		}
		allowedRoots = []string{home}
	}
	out := make([]string, 0, len(allowedRoots))
	seen := map[string]bool{}
	for _, root := range allowedRoots {
		if strings.TrimSpace(root) == "" {
			continue
		}
		canonical, key := pathutil.Canonical(root)
		info, err := os.Stat(canonical)
		if err != nil || !info.IsDir() {
			return nil, errors.New("allowed-root-invalid: " + root)
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, canonical)
	}
	if len(out) == 0 {
		return nil, ErrNoAllowedRoots
	}
	return rooted{allowed: out}, nil
}

// AllowedRoots returns a copy of the canonical allowlist.
func (f rooted) AllowedRoots() []string {
	out := make([]string, len(f.allowed))
	copy(out, f.allowed)
	return out
}

// Check validates the allowlist boundary for a path that may not exist yet.
func (f rooted) Check(path string) error {
	canonical, _ := pathutil.Canonical(path)
	if !f.allows(canonical) {
		return ErrPathOutsideAllowedRoots
	}
	return nil
}

// allows reports whether a canonical path lies inside an allowed root.
func (f rooted) allows(canonical string) bool {
	for _, root := range f.allowed {
		if pathutil.Within(canonical, root) {
			return true
		}
	}
	return false
}

func (f rooted) List(_ context.Context, dir string) ([]Entry, error) {
	if dir == "" {
		return nil, ErrNotADirectory
	}
	canonical, _ := pathutil.Canonical(dir)
	if !f.allows(canonical) {
		return nil, ErrPathOutsideAllowedRoots
	}
	info, err := os.Stat(canonical)
	if err != nil {
		if errors.Is(err, iofs.ErrNotExist) {
			return nil, ErrNotFound
		}
		return nil, ErrNotADirectory
	}
	if !info.IsDir() {
		return nil, ErrNotADirectory
	}

	entries, err := os.ReadDir(canonical)
	if err != nil {
		if errors.Is(err, iofs.ErrNotExist) {
			return nil, ErrNotFound
		}
		return nil, ErrReadFailed
	}

	out := make([]Entry, 0, len(entries))
	for _, e := range entries {
		out = append(out, Entry{
			Name:  e.Name(),
			Path:  filepath.Join(canonical, e.Name()),
			IsDir: e.IsDir(),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}

func (f rooted) Read(_ context.Context, path string, maxBytes int) ([]byte, error) {
	if path == "" {
		return nil, ErrNotFound
	}
	canonical, _ := pathutil.Canonical(path)
	if !f.allows(canonical) {
		return nil, ErrPathOutsideAllowedRoots
	}
	info, err := os.Stat(canonical)
	if err != nil {
		if errors.Is(err, iofs.ErrNotExist) {
			return nil, ErrNotFound
		}
		return nil, ErrReadFailed
	}
	if info.IsDir() {
		return nil, ErrNotRegular
	}
	if !info.Mode().IsRegular() {
		return nil, ErrNotRegular
	}

	file, err := os.Open(canonical)
	if err != nil {
		if errors.Is(err, iofs.ErrNotExist) {
			return nil, ErrNotFound
		}
		return nil, ErrReadFailed
	}
	defer file.Close()

	// Bound the read: read maxBytes+1 bytes; if more than maxBytes arrived, the
	// file exceeds the cap (ADR-0035 §1: Read is bounded by maxBytes).
	buf := make([]byte, maxBytes+1)
	n, err := file.Read(buf)
	if err != nil && err != io.EOF {
		return nil, ErrReadFailed
	}
	if n > maxBytes {
		return nil, ErrTooLarge
	}
	return buf[:n], nil
}
