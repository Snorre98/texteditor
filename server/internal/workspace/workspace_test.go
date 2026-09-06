package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

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

	w := New()
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
	w := New()
	_, err := w.List(context.Background(), filepath.Join(t.TempDir(), "nope"))
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
	w := New()
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
	w := New()
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
	w := New()
	_, err := w.Read(context.Background(), f, 5)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
}

func TestReadNotFoundAndNotRegular(t *testing.T) {
	dir := t.TempDir()
	w := New()
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
