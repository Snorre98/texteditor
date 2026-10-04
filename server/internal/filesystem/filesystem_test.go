package filesystem

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// newTestFS returns a Filesystem bounded by dir (the temp vault).
func newTestFS(t *testing.T, dir string) Filesystem {
	t.Helper()
	f, err := New([]string{dir})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestListShallowSorted(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"B.txt", "a.txt", ".hidden", "Zdir"} {
		p := filepath.Join(dir, name)
		if name == "Zdir" {
			if err := os.Mkdir(p, 0o755); err != nil {
				t.Fatal(err)
			}
			// A child inside Zdir must NOT appear (shallow).
			if err := os.WriteFile(filepath.Join(p, "child.md"), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	w := newTestFS(t, dir)
	entries, err := w.List(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}

	// Hidden dotfiles returned (client filters for display).
	seen := map[string]bool{}
	for _, e := range entries {
		seen[e.Name] = true
	}
	if !seen[".hidden"] {
		t.Fatalf("dotfile not returned: %+v", entries)
	}
	if seen["child.md"] {
		t.Fatalf("shallow list leaked a subdir child: %+v", entries)
	}
	// Case-insensitive sort: .hidden first, then a.txt, B.txt, Zdir.
	want := []string{".hidden", "a.txt", "B.txt", "Zdir"}
	if len(entries) != len(want) {
		t.Fatalf("entries = %d, want %d (%+v)", len(entries), len(want), entries)
	}
	for i, n := range want {
		if entries[i].Name != n {
			t.Fatalf("entry[%d].Name = %q, want %q", i, entries[i].Name, n)
		}
	}
	// IsDir flag correctness.
	for _, e := range entries {
		if e.Name == "Zdir" && !e.IsDir {
			t.Fatalf("Zdir should be IsDir: %+v", e)
		}
		if e.Name == "a.txt" && e.IsDir {
			t.Fatalf("a.txt should not be IsDir: %+v", e)
		}
	}
}

func TestListNotFound(t *testing.T) {
	dir := t.TempDir()
	w := newTestFS(t, dir)
	_, err := w.List(context.Background(), filepath.Join(dir, "nope"))
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestListNotADirectory(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	w := newTestFS(t, dir)
	_, err := w.List(context.Background(), f)
	if !errors.Is(err, ErrNotADirectory) {
		t.Fatalf("err = %v, want ErrNotADirectory", err)
	}
}

func TestReadRawBytes(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "note.md")
	content := []byte("hello mention world")
	if err := os.WriteFile(f, content, 0o644); err != nil {
		t.Fatal(err)
	}
	w := newTestFS(t, dir)
	b, err := w.Read(context.Background(), f, 256*1024)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != string(content) {
		t.Fatalf("read = %q, want %q", b, content)
	}
}

func TestReadTooLarge(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "big.md")
	if err := os.WriteFile(f, []byte("1234567890"), 0o644); err != nil {
		t.Fatal(err)
	}
	w := newTestFS(t, dir)
	_, err := w.Read(context.Background(), f, 5)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
}

func TestReadNotFoundAndNotRegular(t *testing.T) {
	dir := t.TempDir()
	w := newTestFS(t, dir)
	if _, err := w.Read(context.Background(), filepath.Join(dir, "nope.md"), 1024); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Read(context.Background(), sub, 1024); !errors.Is(err, ErrNotRegular) {
		t.Fatalf("err = %v, want ErrNotRegular", err)
	}
}

// TestListAndReadOutsideAllowedRootsRefused covers ADR-0049 §6: a path outside
// the allowlist is a typed refusal, never silent.
func TestListAndReadOutsideAllowedRootsRefused(t *testing.T) {
	allowed := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	w := newTestFS(t, allowed)

	if _, err := w.List(context.Background(), outside); !errors.Is(err, ErrPathOutsideAllowedRoots) {
		t.Fatalf("List outside: err = %v, want ErrPathOutsideAllowedRoots", err)
	}
	if _, err := w.Read(context.Background(), filepath.Join(outside, "secret.md"), 1024); !errors.Is(err, ErrPathOutsideAllowedRoots) {
		t.Fatalf("Read outside: err = %v, want ErrPathOutsideAllowedRoots", err)
	}
}

// TestSymlinkEscapeRefused covers ADR-0047 §2/ADR-0049 §6: a symlink inside an
// allowed root pointing outside is resolved and refused.
func TestSymlinkEscapeRefused(t *testing.T) {
	allowed := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "secret.md")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(allowed, "escape.md")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	w := newTestFS(t, allowed)
	if _, err := w.Read(context.Background(), link, 1024); !errors.Is(err, ErrPathOutsideAllowedRoots) {
		t.Fatalf("symlink escape: err = %v, want ErrPathOutsideAllowedRoots", err)
	}
}

// TestSymlinkInsideAllowedRootResolves: an alias inside the boundary resolves
// to the same content (canonicalization, ADR-0047 §2).
func TestSymlinkInsideAllowedRootResolves(t *testing.T) {
	allowed := t.TempDir()
	target := filepath.Join(allowed, "real.md")
	if err := os.WriteFile(target, []byte("real"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(allowed, "alias.md")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	w := newTestFS(t, allowed)
	b, err := w.Read(context.Background(), link, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "real" {
		t.Fatalf("read = %q, want real", b)
	}
}

// TestAllowedRootsCanonicalized: construction canonicalizes the allowlist and
// exposes it for the typed refusal payload.
func TestAllowedRootsCanonicalized(t *testing.T) {
	dir := t.TempDir()
	w := newTestFS(t, dir)
	roots := w.AllowedRoots()
	if len(roots) != 1 {
		t.Fatalf("roots = %v", roots)
	}
	if _, err := os.Stat(roots[0]); err != nil {
		t.Fatalf("root not canonical/existing: %v", err)
	}
}

// TestNewRejectsMissingRoot: fail-fast startup validation.
func TestNewRejectsMissingRoot(t *testing.T) {
	if _, err := New([]string{filepath.Join(t.TempDir(), "missing")}); err == nil {
		t.Fatal("expected error for missing allowed root")
	}
}
