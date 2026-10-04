package workspace

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"texteditor/internal/pathutil"
	"texteditor/shared/dto"
)

func newTestStore(t *testing.T) Interface {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	return New(db)
}

func TestResolveOrCreateExactResume(t *testing.T) {
	s := newTestStore(t)
	dir := t.TempDir()
	a, err := s.ResolveOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.ResolveOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if a.ID != b.ID {
		t.Fatalf("resume minted a new workspace: %s != %s", a.ID, b.ID)
	}
	if a.Name != filepath.Base(a.Root) {
		t.Fatalf("name = %q, want basename of root", a.Name)
	}
}

func TestResolveOrCreateCanonicalAlias(t *testing.T) {
	s := newTestStore(t)
	dir := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(dir, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	a, err := s.ResolveOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.ResolveOrCreate(alias)
	if err != nil {
		t.Fatal(err)
	}
	if a.ID != b.ID {
		t.Fatalf("alias resolved to a different workspace: %s != %s", a.ID, b.ID)
	}
}

// TestResolveOrCreateNestedResolvesToMostSpecific covers ADR-0049 §2: opening a
// nested directory resolves to the existing containing workspace.
func TestResolveOrCreateNestedResolvesToMostSpecific(t *testing.T) {
	s := newTestStore(t)
	root := t.TempDir()
	nested := filepath.Join(root, "chapters", "one")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	parent, err := s.ResolveOrCreate(root)
	if err != nil {
		t.Fatal(err)
	}
	child, err := s.ResolveOrCreate(nested)
	if err != nil {
		t.Fatal(err)
	}
	if child.ID != parent.ID {
		t.Fatalf("nested directory created a second workspace: %s != %s", child.ID, parent.ID)
	}
	if got, err := s.List(); err != nil || len(got) != 1 {
		t.Fatalf("workspaces = %+v, err %v; want exactly 1", got, err)
	}
}

// TestFindContainingMostSpecific inserts a nested workspace directly (explicit
// nested creation is deferred by ADR-0049) to prove most-specific selection.
func TestFindContainingMostSpecific(t *testing.T) {
	s := newTestStore(t)
	root := t.TempDir()
	nested := filepath.Join(root, "sub")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	parent, err := s.ResolveOrCreate(root)
	if err != nil {
		t.Fatal(err)
	}
	canonicalNested, key := pathutil.Canonical(nested)
	st := s.(*store)
	if _, err := st.db.Exec(
		`INSERT INTO workspaces (id, root, root_key, name, created_at, updated_at) VALUES ('nested', ?, ?, 'nested', 0, 0)`,
		canonicalNested, key,
	); err != nil {
		t.Fatal(err)
	}

	got, ok, err := s.FindContaining(filepath.Join(nested, "file.md"))
	if err != nil || !ok {
		t.Fatalf("FindContaining: %v ok=%v", err, ok)
	}
	if got.ID != "nested" {
		t.Fatalf("most specific = %s, want nested", got.ID)
	}
	// A file directly under root still resolves to the parent.
	got, ok, err = s.FindContaining(filepath.Join(root, "top.md"))
	if err != nil || !ok || got.ID != parent.ID {
		t.Fatalf("root file resolved to %s (ok=%v err=%v), want %s", got.ID, ok, err, parent.ID)
	}
}

func TestResolveOrCreateSeedsDefaultScope(t *testing.T) {
	s := newTestStore(t)
	ws, err := s.ResolveOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	scope, err := s.Scope(ws.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(scope.Roots) != 1 || scope.Roots[0] != ws.Root {
		t.Fatalf("default roots = %v, want [%s]", scope.Roots, ws.Root)
	}
	if len(scope.Include) != 1 || scope.Include[0] != dto.DefaultCorpusInclude {
		t.Fatalf("default include = %v, want [%s]", scope.Include, dto.DefaultCorpusInclude)
	}
}

func TestSetScopeIdempotentAndCanonical(t *testing.T) {
	s := newTestStore(t)
	ws, err := s.ResolveOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(other, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	scope := dto.CorpusScope{
		Roots:   []string{ws.Root, alias, other}, // alias + target dedupe to one
		Include: []string{"**/*.md", "notes/**"},
		Exclude: []string{"drafts/**"},
	}
	first, err := s.SetScope(ws.ID, scope)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Roots) != 2 {
		t.Fatalf("deduped roots = %v, want 2", first.Roots)
	}
	second, err := s.SetScope(ws.ID, scope)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Roots) != len(first.Roots) || len(second.Include) != len(first.Include) || len(second.Exclude) != len(first.Exclude) {
		t.Fatalf("re-applying the same scope changed it: %+v vs %+v", first, second)
	}
}

func TestGetNotFound(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Get("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if _, err := s.ResolveOrCreate(filepath.Join(t.TempDir(), "missing")); !errors.Is(err, ErrRootUnavailable) {
		t.Fatalf("err = %v, want ErrRootUnavailable", err)
	}
}

func TestJobs(t *testing.T) {
	s := newTestStore(t)
	ws, err := s.ResolveOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	job, err := s.StartJob(ws.ID, "index", 3)
	if err != nil {
		t.Fatal(err)
	}
	if job.State != "running" || job.Total != 3 {
		t.Fatalf("job = %+v", job)
	}
	if err := s.UpdateJob(job.ID, 3, "done", ""); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.LatestJob(ws.ID)
	if err != nil || !ok {
		t.Fatalf("LatestJob: %v ok=%v", err, ok)
	}
	if got.State != "done" || got.Completed != 3 || got.FinishedAt == 0 {
		t.Fatalf("job = %+v", got)
	}
}

func TestRoutes(t *testing.T) {
	s := newTestStore(t)
	ws, err := s.ResolveOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RouteSession("sess1", ws.ID); err != nil {
		t.Fatal(err)
	}
	gotWS, ok, err := s.SessionWorkspace("sess1")
	if err != nil || !ok || gotWS != ws.ID {
		t.Fatalf("SessionWorkspace = %q ok=%v err=%v", gotWS, ok, err)
	}
	if err := s.RouteTurn("turn1", ws.ID, "sess1"); err != nil {
		t.Fatal(err)
	}
	wsID, sessID, ok, err := s.TurnRoute("turn1")
	if err != nil || !ok || wsID != ws.ID || sessID != "sess1" {
		t.Fatalf("TurnRoute = %q %q ok=%v err=%v", wsID, sessID, ok, err)
	}
	if _, _, ok, _ := s.TurnRoute("missing"); ok {
		t.Fatal("unknown turn must not resolve")
	}
}
