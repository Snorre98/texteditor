// Package workspace holds the Workspace module — read-only filesystem reach
// (ADR-0035). It owns a shallow, non-recursive directory listing and bounded raw
// file reads, keeping stateless filesystem access distinct from the versioning
// Document store. A pure leaf: no state, no database, no cache; its hidden
// internals are os.ReadDir/os.ReadFile plus path validation.
package workspace

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Entry is one directory entry (interface.md §9b, ADR-0035 §1).
type Entry struct {
	Name  string // bare file/dir name
	Path  string // absolute path
	IsDir bool
}

// Workspace is the Workspace public API (interface.md §9b).
type Workspace interface {
	// List returns the direct, non-recursive children of dir, sorted by name
	// (case-insensitive). Hidden entries (dotfiles) are returned; filtering for
	// display is client-side presentation (ADR-0035 §1).
	List(ctx context.Context, dir string) ([]Entry, error)
	// Read returns at most maxBytes of raw file content. It never registers,
	// versions, or indexes anything — mentioned-file context reads through here
	// and is provably side-effect-free (ADR-0036 §6).
	Read(ctx context.Context, path string, maxBytes int) ([]byte, error)
}

// Interface is an alias for Workspace (the contracted name, interface.md §9b).
type Interface = Workspace

// Typed errors (interface.md §9b, ADR-0035 §1). The loop maps these to the
// mention SSE codes; the API server maps List failures to not-found /
// not-a-directory.
var (
	ErrNotFound      = errors.New("not-found")
	ErrNotADirectory = errors.New("not-a-directory")
	ErrNotRegular    = errors.New("not-regular")
	ErrTooLarge      = errors.New("too-large")
	ErrReadFailed    = errors.New("read-failed")
)

// ws is the concrete Workspace (a pure leaf, no out-edges).
type ws struct{}

// New returns the default Workspace.
func New() Workspace { return ws{} }

func (ws) List(_ context.Context, dir string) ([]Entry, error) {
	if dir == "" {
		return nil, ErrNotADirectory
	}
	info, err := os.Stat(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, ErrNotFound
		}
		return nil, ErrNotADirectory
	}
	if !info.IsDir() {
		return nil, ErrNotADirectory
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, ErrNotFound
		}
		return nil, ErrReadFailed
	}

	out := make([]Entry, 0, len(entries))
	for _, e := range entries {
		out = append(out, Entry{
			Name:  e.Name(),
			Path:  filepath.Join(dir, e.Name()),
			IsDir: e.IsDir(),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}

func (ws) Read(_ context.Context, path string, maxBytes int) ([]byte, error) {
	if path == "" {
		return nil, ErrNotFound
	}
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
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

	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, ErrNotFound
		}
		return nil, ErrReadFailed
	}
	defer f.Close()

	// Bound the read: read maxBytes+1 bytes; if more than maxBytes arrived, the
	// file exceeds the cap (ADR-0035 §1: Read is bounded by maxBytes).
	buf := make([]byte, maxBytes+1)
	n, err := f.Read(buf)
	if err != nil && err != io.EOF {
		return nil, ErrReadFailed
	}
	if n > maxBytes {
		return nil, ErrTooLarge
	}
	return buf[:n], nil
}
