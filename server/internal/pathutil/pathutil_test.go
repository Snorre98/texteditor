package pathutil

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCanonicalCaseFolds(t *testing.T) {
	_, key := Canonical("/Vault/Notes/File.MD")
	if key != "/vault/notes/file.md" {
		t.Fatalf("key = %q", key)
	}
}

func TestCanonicalResolvesSymlinks(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real.md")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "alias.md")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	want, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	canonical, _ := Canonical(link)
	if canonical != want {
		t.Fatalf("canonical = %q, want %q", canonical, want)
	}
}

func TestWithin(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "vault")
	cases := []struct {
		path string
		want bool
	}{
		{filepath.Join(root, "a.md"), true},
		{filepath.Join(root, "sub", "b.md"), true},
		{root, true},
		{filepath.Join(string(filepath.Separator), "vault2", "c.md"), false},
		{filepath.Join(string(filepath.Separator), "other"), false},
	}
	for _, c := range cases {
		if got := Within(c.path, root); got != c.want {
			t.Errorf("Within(%q, %q) = %v, want %v", c.path, root, got, c.want)
		}
	}
}

func TestDocIDStable(t *testing.T) {
	a := DocID("/vault/a.md")
	b := DocID("/vault/a.md")
	if a != b || len(a) != 32 {
		t.Fatalf("DocID = %q / %q", a, b)
	}
	if DocID("/vault/b.md") == a {
		t.Fatal("distinct paths must have distinct ids")
	}
}

func TestHash(t *testing.T) {
	if Hash("x") == "" || Hash("x") == Hash("y") {
		t.Fatal("hash must be non-empty and content-sensitive")
	}
}
