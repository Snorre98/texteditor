package apiserver

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"texteditor/internal/chunker"
	"texteditor/internal/corpus"
	"texteditor/internal/filesystem"
	"texteditor/internal/retriever"
	"texteditor/internal/shard"
	"texteditor/internal/workspace"
	"texteditor/shared/dto"
)

// ------------------------- retriever stubs (no model needed) -------------------------

type e2eResolver struct{}

func (e2eResolver) Resolve(name string, _ dto.ResolveOpts) (dto.Resolution, error) {
	return dto.Resolution{Model: dto.Model{Name: name, BaseURL: "http://stub"}}, nil
}

type e2eEmbedder struct{}

func (e2eEmbedder) Embed(context.Context, dto.Target, string) ([]float32, error) {
	return []float32{1, 0, 0, 0, 0, 0, 0, 0}, nil
}

type e2eDocs struct{}

func (e2eDocs) Blocks(string) ([]dto.Block, error) { return nil, nil }
func (e2eDocs) Path(string) (string, error)        { return "", errors.New("no versioned docs in this E2E") }

// newCorpusE2EServer wires the real stores (workspaces.db, shard manager, real
// retriever with stub embedding, real corpus service) behind the HTTP surface.
func newCorpusE2EServer(t *testing.T) (*Server, string) {
	t.Helper()
	dataDir := t.TempDir()
	allowed := t.TempDir()

	wsDB, err := sql.Open("sqlite", filepath.Join(dataDir, "workspaces.db"))
	if err != nil {
		t.Fatal(err)
	}
	wsDB.SetMaxOpenConns(1)
	t.Cleanup(func() { wsDB.Close() })
	if err := workspace.Migrate(context.Background(), wsDB); err != nil {
		t.Fatal(err)
	}
	wsStore := workspace.New(wsDB)

	fsGW, err := filesystem.New([]string{allowed})
	if err != nil {
		t.Fatal(err)
	}
	shards := shard.New(shard.Options{
		DataDir: dataDir,
		NewRetriever: func(db *sql.DB) retriever.Interface {
			return retriever.New(db, e2eResolver{}, e2eEmbedder{}, e2eDocs{}, fsGW, chunker.New(), 512)
		},
	})
	t.Cleanup(func() { shards.Close() })
	corpusSvc := corpus.New(corpus.Options{Workspaces: wsStore, Filesystem: fsGW, Shards: shards})

	bus := &fakeBus{}
	srv, err := New(Deps{
		Fleet:      stubFleet{},
		Modes:      stubModes{},
		Tools:      stubTools{},
		Doc:        stubDoc{},
		Loop:       &stubLoopEmitter{bus: bus},
		Filesystem: fsGW,
		Workspaces: wsStore,
		Shards:     shards,
		Corpus:     corpusSvc,
	}, bus)
	if err != nil {
		t.Fatal(err)
	}
	return srv, allowed
}

// doJSON performs one JSON request and returns the decoded body.
func doJSON(t *testing.T, srv *Server, method, path string, body interface{}) (int, map[string]interface{}) {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(b)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	out := map[string]interface{}{}
	if rec.Body.Len() > 0 {
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
	}
	return rec.Code, out
}

// pollCorpus waits for the workspace's latest job to leave "running".
func pollCorpus(t *testing.T, srv *Server, workspaceID string) map[string]interface{} {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		code, state := doJSON(t, srv, http.MethodGet, "/corpus?workspaceId="+workspaceID, nil)
		if code != http.StatusOK {
			t.Fatalf("GET /corpus = %d %v", code, state)
		}
		if job, ok := state["job"].(map[string]interface{}); ok {
			if job["state"] != "running" {
				return state
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("corpus job did not finish")
	return nil
}

// docByPath finds one document row by path suffix.
func docByPath(t *testing.T, state map[string]interface{}, suffix string) map[string]interface{} {
	t.Helper()
	docs, _ := state["documents"].([]interface{})
	for _, d := range docs {
		doc, _ := d.(map[string]interface{})
		if p, _ := doc["path"].(string); filepath.Base(p) == suffix || len(p) >= len(suffix) && p[len(p)-len(suffix):] == suffix {
			return doc
		}
	}
	t.Fatalf("document %q not found in %v", suffix, docs)
	return nil
}

// TestCorpusHTTPE2E is the C2 gate: real stores + temp vaults over HTTP,
// covering workspace registration, scope set, async index, per-document status,
// idempotent eviction, and the ALLOWED_ROOTS typed refusal.
func TestCorpusHTTPE2E(t *testing.T) {
	srv, root := newCorpusE2EServer(t)

	mustWriteFile(t, filepath.Join(root, "a.md"), "# A\n\nalpha")
	mustWriteFile(t, filepath.Join(root, "sub", "b.md"), "# B\n\nbeta")
	mustWriteFile(t, filepath.Join(root, ".obsidian", "hidden.md"), "# H\n\nhidden")
	outside := t.TempDir()
	mustWriteFile(t, filepath.Join(outside, "outside.md"), "# O\n\noutside")

	// Workspace create-or-resume.
	code, ws := doJSON(t, srv, http.MethodPost, "/workspaces", map[string]interface{}{"root": root})
	if code != http.StatusOK {
		t.Fatalf("POST /workspaces = %d %v", code, ws)
	}
	wsID, _ := ws["id"].(string)
	if wsID == "" || ws["root"] == "" {
		t.Fatalf("workspace = %v", ws)
	}

	// Scope: the workspace root plus its default include.
	code, state := doJSON(t, srv, http.MethodPut, "/corpus", map[string]interface{}{
		"workspaceId": wsID,
		"roots":       []string{root},
		"include":     []string{"**/*.md"},
	})
	if code != http.StatusOK {
		t.Fatalf("PUT /corpus = %d %v", code, state)
	}

	// Async index + poll.
	code, job := doJSON(t, srv, http.MethodPost, "/corpus/index", map[string]interface{}{"workspaceId": wsID})
	if code != http.StatusAccepted {
		t.Fatalf("POST /corpus/index = %d %v", code, job)
	}
	state = pollCorpus(t, srv, wsID)

	docs, _ := state["documents"].([]interface{})
	if len(docs) != 2 {
		t.Fatalf("documents = %v, want 2 (hidden dir excluded)", docs)
	}
	a := docByPath(t, state, "a.md")
	if a["status"] != "indexed" || a["chunkCount"].(float64) == 0 {
		t.Fatalf("a.md = %v, want indexed with chunks", a)
	}
	if docByPath(t, state, "b.md")["status"] != "indexed" {
		t.Fatalf("b.md not indexed: %v", state)
	}
	for _, d := range docs {
		if doc, _ := d.(map[string]interface{}); doc["path"] == filepath.Join(root, ".obsidian", "hidden.md") {
			t.Fatalf("hidden file indexed: %v", doc)
		}
	}

	// Stale detection: change the file on disk without re-indexing.
	mustWriteFile(t, filepath.Join(root, "a.md"), "# A\n\nalpha changed")
	_, state = doJSON(t, srv, http.MethodGet, "/corpus?workspaceId="+wsID, nil)
	if got := docByPath(t, state, "a.md")["status"]; got != "stale" {
		t.Fatalf("a.md status = %v, want stale", got)
	}

	// Idempotent eviction.
	aID, _ := docByPath(t, state, "a.md")["id"].(string)
	for i := 0; i < 2; i++ {
		code, _ := doJSON(t, srv, http.MethodDelete, "/corpus/documents/"+aID+"?workspaceId="+wsID, nil)
		if code != http.StatusNoContent {
			t.Fatalf("DELETE #%d = %d, want 204", i+1, code)
		}
	}
	_, state = doJSON(t, srv, http.MethodGet, "/corpus?workspaceId="+wsID, nil)
	if got := docByPath(t, state, "a.md")["status"]; got != "evicted" {
		t.Fatalf("a.md status = %v, want evicted", got)
	}

	// Rebuild re-indexes and clears the tombstone.
	doJSON(t, srv, http.MethodPost, "/corpus/index", map[string]interface{}{"workspaceId": wsID})
	state = pollCorpus(t, srv, wsID)
	if got := docByPath(t, state, "a.md")["status"]; got != "indexed" {
		t.Fatalf("a.md status after rebuild = %v, want indexed", got)
	}

	// ALLOWED_ROOTS refusal: corpus mutation and directory browsing.
	code, refusal := doJSON(t, srv, http.MethodPut, "/corpus", map[string]interface{}{
		"workspaceId": wsID,
		"roots":       []string{outside},
		"include":     []string{"**/*.md"},
	})
	if code != http.StatusForbidden {
		t.Fatalf("PUT /corpus outside = %d %v, want 403", code, refusal)
	}
	if refusal["error"] != "path-outside-allowed-roots" {
		t.Fatalf("refusal = %v", refusal)
	}
	code, refusal = doJSON(t, srv, http.MethodGet, "/directories?path="+outside, nil)
	if code != http.StatusForbidden || refusal["error"] != "path-outside-allowed-roots" {
		t.Fatalf("GET /directories outside = %d %v, want typed 403", code, refusal)
	}
	if roots, _ := refusal["allowedRoots"].([]interface{}); len(roots) == 0 {
		t.Fatalf("refusal lost the allowlist: %v", refusal)
	}

	// Workspace registry list/get.
	code, _ = doJSON(t, srv, http.MethodGet, "/workspaces", nil)
	if code != http.StatusOK {
		t.Fatalf("GET /workspaces = %d", code)
	}
	code, got := doJSON(t, srv, http.MethodGet, "/workspaces/"+wsID, nil)
	if code != http.StatusOK || got["id"] != wsID {
		t.Fatalf("GET /workspaces/{id} = %d %v", code, got)
	}
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
