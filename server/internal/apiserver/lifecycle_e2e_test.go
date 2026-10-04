package apiserver

// E2E tests for the engine-owned lifecycle verbs (ADR-0052 §1–§3): one-verb
// bootstrap, session open-or-resume, and atomic accept. They wire the real
// document, workspace, and session stores behind the HTTP surface (no model
// needed — accept is deterministic HTTP).

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"texteditor/internal/document"
	"texteditor/internal/filesystem"
	"texteditor/internal/session"
	"texteditor/internal/shard"
	"texteditor/internal/textformatter"
	"texteditor/internal/workspace"
	"texteditor/shared/dto"
)

// fixedShards resolves every workspace to one shard carrying a real session
// store (the lifecycle verbs never touch the retriever/meter).
type fixedShards struct{ svc *shard.Services }

func (f fixedShards) Services(context.Context, string) (*shard.Lease, error) {
	return shard.NewLease(*f.svc), nil
}

// newLifecycleServer wires the real stores behind the API server and returns it
// plus the allowed root under which test vaults must live.
func newLifecycleServer(t *testing.T) (*Server, string) {
	t.Helper()
	dataDir := t.TempDir()
	allowed := t.TempDir()
	ctx := context.Background()

	open := func(name string) *sql.DB {
		db, err := sql.Open("sqlite", filepath.Join(dataDir, name))
		if err != nil {
			t.Fatal(err)
		}
		db.SetMaxOpenConns(1)
		t.Cleanup(func() { db.Close() })
		return db
	}

	appDB := open("app.db")
	if err := document.Migrate(ctx, appDB); err != nil {
		t.Fatal(err)
	}
	docStore, err := document.NewStore(appDB,
		filepath.Join(dataDir, "git"), filepath.Join(dataDir, "worktree"), textformatter.New())
	if err != nil {
		t.Fatal(err)
	}

	wsDB := open("workspaces.db")
	if err := workspace.Migrate(ctx, wsDB); err != nil {
		t.Fatal(err)
	}
	wsStore := workspace.New(wsDB)

	sessDB := open("sessions.db")
	if err := session.Migrate(ctx, sessDB); err != nil {
		t.Fatal(err)
	}
	sessStore := session.New(sessDB)

	fsGW, err := filesystem.New([]string{allowed})
	if err != nil {
		t.Fatal(err)
	}

	srv, err := New(Deps{
		Fleet:      stubFleet{},
		Modes:      stubModes{modes: []dto.Mode{{Name: "proofreader", DefaultModel: "gemma4-12b"}}},
		Tools:      stubTools{},
		Doc:        docStore,
		Filesystem: fsGW,
		Workspaces: wsStore,
		Shards:     fixedShards{svc: &shard.Services{Sessions: sessStore}},
		Loop:       &stubLoopEmitter{bus: &fakeBus{}},
	}, &fakeBus{})
	if err != nil {
		t.Fatal(err)
	}
	return srv, allowed
}

// writeFile creates a file under dir with content.
func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestOpenDirectoryReturnsBoundedListing(t *testing.T) {
	srv, allowed := newLifecycleServer(t)
	vault := filepath.Join(allowed, "vault")
	if err := os.MkdirAll(vault, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, vault, "note.md", "hello\n")

	code, body := doJSON(t, srv, "POST", "/open", map[string]any{"path": vault})
	if code != 200 {
		t.Fatalf("POST /open = %d %v", code, body)
	}
	if body["kind"] != "directory" {
		t.Fatalf("kind = %v, want directory", body["kind"])
	}
	listing, ok := body["listing"].(map[string]interface{})
	if !ok {
		t.Fatalf("listing missing: %v", body)
	}
	entries, _ := listing["entries"].([]interface{})
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	ws, _ := body["workspace"].(map[string]interface{})
	if ws == nil || ws["id"] == nil || ws["id"] == "" {
		t.Fatalf("workspace missing id: %v", body["workspace"])
	}
}

func TestOpenFileReturnsDocumentContext(t *testing.T) {
	srv, allowed := newLifecycleServer(t)
	vault := filepath.Join(allowed, "vault")
	if err := os.MkdirAll(vault, 0o755); err != nil {
		t.Fatal(err)
	}
	path := writeFile(t, vault, "note.md", "# Title\n\nThe original sentence.\n")

	code, body := doJSON(t, srv, "POST", "/open", map[string]any{"path": path})
	if code != 200 {
		t.Fatalf("POST /open = %d %v", code, body)
	}
	if body["kind"] != "document" {
		t.Fatalf("kind = %v, want document", body["kind"])
	}
	if _, ok := body["document"].(map[string]interface{}); !ok {
		t.Fatalf("document missing: %v", body)
	}
	blocks, _ := body["blocks"].([]interface{})
	if len(blocks) == 0 {
		t.Fatalf("blocks empty: %v", body)
	}
	if _, ok := body["session"].(map[string]interface{}); !ok {
		t.Fatalf("session missing: %v", body)
	}
	modes, _ := body["modes"].([]interface{})
	if len(modes) == 0 {
		t.Fatalf("modes empty: %v", body)
	}
	// The workspace is rooted at the document's parent directory.
	ws, _ := body["workspace"].(map[string]interface{})
	canonicalVault, _ := filepath.EvalSymlinks(vault)
	if ws["root"] != canonicalVault {
		t.Fatalf("workspace.root = %v, want %v", ws["root"], canonicalVault)
	}
}

func TestOpenDocumentSessionResumesNewest(t *testing.T) {
	srv, allowed := newLifecycleServer(t)
	vault := filepath.Join(allowed, "vault")
	if err := os.MkdirAll(vault, 0o755); err != nil {
		t.Fatal(err)
	}
	path := writeFile(t, vault, "note.md", "hello\n")

	_, openBody := doJSON(t, srv, "POST", "/open", map[string]any{"path": path})
	documentID := openBody["document"].(map[string]interface{})["id"].(string)

	code, first := doJSON(t, srv, "POST", "/documents/"+documentID+"/session", nil)
	if code != 200 {
		t.Fatalf("session = %d %v", code, first)
	}
	firstID := first["id"].(string)

	// A second call resumes the same (newest) session.
	_, second := doJSON(t, srv, "POST", "/documents/"+documentID+"/session", nil)
	if second["id"] != firstID {
		t.Fatalf("session id changed: %v vs %v", second["id"], firstID)
	}

	// An anchor selects a distinct anchor-keyed session.
	anchor := "b-anchor"
	_, anchored := doJSON(t, srv, "POST", "/documents/"+documentID+"/session",
		map[string]any{"anchorBlockId": anchor})
	if anchored["id"] == firstID {
		t.Fatalf("anchor returned the doc-level session")
	}
	if anchored["anchorBlockId"] != anchor {
		t.Fatalf("anchorBlockId = %v, want %v", anchored["anchorBlockId"], anchor)
	}
}

func TestAcceptBlockWritesThrough(t *testing.T) {
	srv, allowed := newLifecycleServer(t)
	vault := filepath.Join(allowed, "vault")
	if err := os.MkdirAll(vault, 0o755); err != nil {
		t.Fatal(err)
	}
	path := writeFile(t, vault, "note.md", "The original sentence.\n")

	_, openBody := doJSON(t, srv, "POST", "/open", map[string]any{"path": path})
	doc := openBody["document"].(map[string]interface{})
	documentID := doc["id"].(string)
	canonicalPath := doc["path"].(string)
	blocks := openBody["blocks"].([]interface{})
	blockID := blocks[0].(map[string]interface{})["id"].(string)

	// Stage a candidate (as a turn's edit_markdown would).
	code, _ := doJSON(t, srv, "POST", "/documents/"+documentID+"/edits",
		map[string]any{"blockId": blockID, "text": "The rewritten sentence."})
	if code != 200 {
		t.Fatalf("stage = %d", code)
	}

	// Accept atomically: commit + write-through, no candidate text sent.
	code, accepted := doJSON(t, srv, "POST",
		"/documents/"+documentID+"/blocks/"+blockID+"/accept", map[string]any{})
	if code != 200 {
		t.Fatalf("accept = %d %v", code, accepted)
	}
	if accepted["writtenThrough"] != true {
		t.Fatalf("writtenThrough = %v, want true", accepted["writtenThrough"])
	}
	if accepted["path"] != canonicalPath {
		t.Fatalf("path = %v, want %v", accepted["path"], canonicalPath)
	}
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(onDisk)) != "The rewritten sentence." {
		t.Fatalf("disk = %q", string(onDisk))
	}
}

func TestAcceptBlockRefusesExternalChange(t *testing.T) {
	srv, allowed := newLifecycleServer(t)
	vault := filepath.Join(allowed, "vault")
	if err := os.MkdirAll(vault, 0o755); err != nil {
		t.Fatal(err)
	}
	path := writeFile(t, vault, "note.md", "The original sentence.\n")

	_, openBody := doJSON(t, srv, "POST", "/open", map[string]any{"path": path})
	documentID := openBody["document"].(map[string]interface{})["id"].(string)
	blockID := openBody["blocks"].([]interface{})[0].(map[string]interface{})["id"].(string)

	// Stage, then change the file externally before accepting.
	doJSON(t, srv, "POST", "/documents/"+documentID+"/edits",
		map[string]any{"blockId": blockID, "text": "The rewritten sentence."})
	writeFile(t, vault, "note.md", "changed externally\n")

	code, body := doJSON(t, srv, "POST",
		"/documents/"+documentID+"/blocks/"+blockID+"/accept", map[string]any{})
	if code != 409 {
		t.Fatalf("accept = %d %v, want 409", code, body)
	}
	if body["error"] != "file-changed-externally" {
		t.Fatalf("error = %v", body["error"])
	}
	onDisk, _ := os.ReadFile(path)
	if string(onDisk) != "changed externally\n" {
		t.Fatalf("external change clobbered: %q", string(onDisk))
	}
}
