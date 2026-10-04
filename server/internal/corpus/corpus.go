// Package corpus holds the Corpus service — the engine-owned, index-only
// retrieval scope over a workspace's multi-root corpus (ADR-0049 §3/§4).
//
// It resolves scope (roots + include/exclude globs) through the Workspace store,
// walks it through the bounded Filesystem leaf (so ALLOWED_ROOTS applies),
// indexes files path-keyed through the workspace shard's Retriever, reconciles
// evictions, and exposes per-document status plus observable job progress.
// Corpus files are never documents rows and never versioned: no open, no
// version, no write — only read-to-chunk-and-embed (ADR-0049 §4).
package corpus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"texteditor/internal/document"
	"texteditor/internal/filesystem"
	"texteditor/internal/glob"
	"texteditor/internal/pathutil"
	"texteditor/internal/retriever"
	"texteditor/internal/shard"
	"texteditor/internal/workspace"
	"texteditor/shared/dto"
)

// Interface is the Corpus service public API (interface.md §9c; ADR-0049 §4).
type Interface interface {
	// Get returns scope + per-document status + the latest job.
	Get(ctx context.Context, workspaceID string) (dto.CorpusState, error)
	// SetScope persists a scope (idempotent) and enqueues an async reconcile.
	SetScope(ctx context.Context, workspaceID string, scope dto.CorpusScope) (dto.CorpusState, error)
	// Index enqueues an async bulk (re)index of the current scope.
	Index(ctx context.Context, workspaceID string) (dto.CorpusJob, error)
	// Evict removes one corpus document (by path-derived id) from retrieval,
	// recording a tombstone. Idempotent.
	Evict(ctx context.Context, workspaceID, documentID string) error
	// NotifyChanged re-indexes one changed path asynchronously if it is in any
	// workspace's corpus. It never blocks the caller (ADR-0049 §4 lifecycle).
	NotifyChanged(path string)
}

// Emitter is the sealed subset of the SSE event bus the Corpus service needs:
// fan-out of non-turn `corpus` feed events (ADR-0052 §4). The bus owns
// subscription (interface.md §11).
type Emitter interface {
	Emit(dto.Event)
}

// Options configures the Corpus service.
type Options struct {
	Workspaces workspace.Interface
	Filesystem filesystem.Interface
	Shards     shard.Resolver
	// Bus, when set, receives `corpus` feed events (job progress/completion,
	// ADR-0052 §4). A nil bus disables emission.
	Bus Emitter
}

// readCap bounds one corpus file read for hashing/indexing (4 MiB).
const readCap = 4 << 20

// service is the concrete Corpus service.
type service struct {
	opts Options

	mu      sync.Mutex
	running map[string]string // workspaceID → running job id (single-flight)
}

// New returns a Corpus service.
func New(opts Options) Interface {
	return &service{opts: opts, running: map[string]string{}}
}

// scopeFile is one in-scope corpus file.
type scopeFile struct {
	path    string // canonical absolute
	pathKey string // case-folded canonical
	rel     string // slash-separated path relative to its root
}

// Get returns the workspace's corpus state.
func (s *service) Get(ctx context.Context, workspaceID string) (dto.CorpusState, error) {
	scope, err := s.opts.Workspaces.Scope(workspaceID)
	if err != nil {
		return dto.CorpusState{}, err
	}
	docs, err := s.status(ctx, workspaceID, scope)
	if err != nil {
		return dto.CorpusState{}, err
	}
	state := dto.CorpusState{
		WorkspaceID: workspaceID,
		Roots:       scope.Roots,
		Include:     scope.Include,
		Exclude:     scope.Exclude,
		Documents:   docs,
	}
	if job, ok, err := s.opts.Workspaces.LatestJob(workspaceID); err != nil {
		return dto.CorpusState{}, err
	} else if ok {
		state.Job = &job
	}
	return state, nil
}

// SetScope validates the roots (ALLOWED_ROOTS + existence), persists the scope
// idempotently, and enqueues an async reconcile.
func (s *service) SetScope(ctx context.Context, workspaceID string, scope dto.CorpusScope) (dto.CorpusState, error) {
	roots := scope.Roots
	if len(roots) == 0 {
		if ws, err := s.opts.Workspaces.Get(workspaceID); err == nil {
			roots = []string{ws.Root}
		}
	}
	for _, root := range roots {
		if err := s.opts.Filesystem.Check(root); err != nil {
			return dto.CorpusState{}, err
		}
		info, err := os.Stat(root)
		if err != nil || (!info.IsDir() && !info.Mode().IsRegular()) {
			return dto.CorpusState{}, fmt.Errorf("corpus-root-invalid: %s", root)
		}
	}
	if _, err := s.opts.Workspaces.SetScope(workspaceID, scope); err != nil {
		return dto.CorpusState{}, err
	}
	if _, err := s.enqueue(ctx, workspaceID, "reconcile"); err != nil {
		return dto.CorpusState{}, err
	}
	return s.Get(ctx, workspaceID)
}

// Index enqueues an async bulk (re)index of the current scope.
func (s *service) Index(ctx context.Context, workspaceID string) (dto.CorpusJob, error) {
	if _, err := s.opts.Workspaces.Get(workspaceID); err != nil {
		return dto.CorpusJob{}, err
	}
	return s.enqueue(ctx, workspaceID, "index")
}

// Evict removes one corpus document from retrieval and records a tombstone.
func (s *service) Evict(ctx context.Context, workspaceID, documentID string) error {
	scope, err := s.opts.Workspaces.Scope(workspaceID)
	if err != nil {
		return err
	}
	files, err := s.walk(scope)
	if err != nil {
		return err
	}
	target := ""
	for _, f := range files {
		if pathutil.DocID(f.path) == documentID {
			target = f.path
			break
		}
	}
	if target == "" {
		// Already evicted (or the file is gone): idempotent no-op.
		return nil
	}
	if err := s.opts.Filesystem.Check(target); err != nil {
		return err
	}
	if err := s.opts.Workspaces.MarkEvicted(workspaceID, target); err != nil {
		return err
	}
	lease, err := s.opts.Shards.Services(ctx, workspaceID)
	if err != nil {
		return err
	}
	defer lease.Release()
	return lease.Retriever.Evict(ctx, target)
}

// NotifyChanged re-indexes one changed path if it is in a workspace's scope.
// Non-blocking by construction.
func (s *service) NotifyChanged(path string) {
	if strings.TrimSpace(path) == "" {
		return
	}
	canonical, key := pathutil.Canonical(path)
	go func() {
		ctx := context.Background()
		workspaces, err := s.opts.Workspaces.List()
		if err != nil {
			return
		}
		for _, ws := range workspaces {
			scope, err := s.opts.Workspaces.Scope(ws.ID)
			if err != nil {
				continue
			}
			if !s.pathInScope(scope, canonical, key) {
				continue
			}
			_, _ = s.enqueue(ctx, ws.ID, "path")
		}
	}()
}

// pathInScope reports whether one canonical path lies in the scope, without a
// directory walk (roots containment + glob match).
func (s *service) pathInScope(scope dto.CorpusScope, canonical, key string) bool {
	for _, root := range scope.Roots {
		_, rootKey := pathutil.Canonical(root)
		info, err := os.Stat(root)
		if err != nil {
			continue
		}
		var rel string
		if !info.IsDir() {
			if rootKey != key {
				continue
			}
			rel = filepath.Base(root)
		} else {
			if !pathutil.Within(canonical, root) {
				continue
			}
			r, err := filepath.Rel(root, canonical)
			if err != nil || strings.HasPrefix(r, "..") {
				continue
			}
			rel = filepath.ToSlash(r)
		}
		if hiddenPath(rel) {
			continue
		}
		if inScope(scope, rel) {
			return true
		}
	}
	return false
}

// enqueue starts (or coalesces onto) an async job for a workspace.
func (s *service) enqueue(ctx context.Context, workspaceID, kind string) (dto.CorpusJob, error) {
	s.mu.Lock()
	if _, ok := s.running[workspaceID]; ok {
		s.mu.Unlock()
		// Coalesce onto the running job.
		if job, found, err := s.opts.Workspaces.LatestJob(workspaceID); err == nil && found {
			return job, nil
		}
		return dto.CorpusJob{}, errors.New("corpus-job-running")
	}
	scope, err := s.opts.Workspaces.Scope(workspaceID)
	if err != nil {
		s.mu.Unlock()
		return dto.CorpusJob{}, err
	}
	files, err := s.walk(scope)
	if err != nil {
		s.mu.Unlock()
		return dto.CorpusJob{}, err
	}
	job, err := s.opts.Workspaces.StartJob(workspaceID, kind, len(files))
	if err != nil {
		s.mu.Unlock()
		return dto.CorpusJob{}, err
	}
	s.running[workspaceID] = job.ID
	s.mu.Unlock()

	s.emitJob(job)
	go s.run(context.Background(), workspaceID, job.ID, kind, files)
	return job, nil
}

// emitJob publishes one `corpus` feed event (ADR-0052 §4). A nil bus is a no-op.
func (s *service) emitJob(job dto.CorpusJob) {
	if s.opts.Bus == nil {
		return
	}
	data, err := json.Marshal(dto.CorpusEventPayload{WorkspaceID: job.WorkspaceID, Job: job})
	if err != nil {
		return
	}
	s.opts.Bus.Emit(dto.Event{WorkspaceID: job.WorkspaceID, Type: dto.EventCorpus, Data: data})
}

// run indexes the given files, reconciles out-of-scope corpus rows on a
// reconcile, and records per-path errors (never silent).
func (s *service) run(ctx context.Context, workspaceID, jobID, kind string, files []scopeFile) {
	defer func() {
		s.mu.Lock()
		delete(s.running, workspaceID)
		s.mu.Unlock()
	}()

	lease, err := s.opts.Shards.Services(ctx, workspaceID)
	if err != nil {
		s.finishJob(workspaceID, jobID, 0, "error", err.Error())
		s.emitJob(dto.CorpusJob{ID: jobID, WorkspaceID: workspaceID, Kind: kind, State: "error", Total: len(files), Error: err.Error(), FinishedAt: time.Now().Unix()})
		return
	}
	defer lease.Release()

	completed := 0
	var firstErr string
	for _, f := range files {
		if err := lease.Retriever.IndexPath(ctx, f.path, ""); err != nil {
			_ = s.opts.Workspaces.MarkError(workspaceID, f.path, err.Error())
			if firstErr == "" {
				firstErr = err.Error()
			}
		} else {
			_ = s.opts.Workspaces.ClearError(workspaceID, f.path)
			_ = s.opts.Workspaces.ClearEvicted(workspaceID, f.path)
		}
		completed++
		_ = s.opts.Workspaces.UpdateJob(jobID, completed, "running", "")
		s.emitJob(dto.CorpusJob{ID: jobID, WorkspaceID: workspaceID, Kind: kind, State: "running", Total: len(files), Completed: completed})
	}

	if kind == "reconcile" {
		if err := s.evictOutOfScope(ctx, lease.Retriever, workspaceID, files); err != nil && firstErr == "" {
			firstErr = err.Error()
		}
	}

	state := "done"
	if firstErr != "" && completed == 0 {
		state = "error"
	}
	s.finishJob(workspaceID, jobID, completed, state, firstErr)
	s.emitJob(dto.CorpusJob{ID: jobID, WorkspaceID: workspaceID, Kind: kind, State: state, Total: len(files), Completed: completed, Error: firstErr, FinishedAt: time.Now().Unix()})
}

// finishJob clears the single-flight entry before publishing the terminal job
// state, so a new enqueue can never coalesce onto a finished job.
func (s *service) finishJob(workspaceID, jobID string, completed int, state, errMsg string) {
	s.mu.Lock()
	delete(s.running, workspaceID)
	s.mu.Unlock()
	_ = s.opts.Workspaces.UpdateJob(jobID, completed, state, errMsg)
}

// evictOutOfScope removes corpus-indexed rows whose path is no longer in scope
// (ADR-0049 §4: excluding a document reconciles by evicting it).
func (s *service) evictOutOfScope(ctx context.Context, rec retriever.Interface, workspaceID string, files []scopeFile) error {
	indexed, err := rec.Status()
	if err != nil {
		return err
	}
	inScope := map[string]bool{}
	for _, f := range files {
		inScope[f.pathKey] = true
	}
	for _, row := range indexed {
		if row.DocumentID != "" {
			continue // versioned documents are not corpus-managed
		}
		_, key := pathutil.Canonical(row.Path)
		if inScope[key] {
			continue
		}
		if err := rec.Evict(ctx, row.Path); err != nil {
			return err
		}
		_ = s.opts.Workspaces.ClearError(workspaceID, row.Path)
	}
	return nil
}

// status computes per-document status for the scope without writing anything.
func (s *service) status(ctx context.Context, workspaceID string, scope dto.CorpusScope) ([]dto.CorpusDocumentStatus, error) {
	files, err := s.walk(scope)
	if err != nil {
		return nil, err
	}
	lease, err := s.opts.Shards.Services(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	defer lease.Release()

	indexed, err := lease.Retriever.Status()
	if err != nil {
		return nil, err
	}
	byKey := map[string]dto.IndexedDocument{}
	for _, row := range indexed {
		if row.DocumentID != "" {
			continue
		}
		_, key := pathutil.Canonical(row.Path)
		byKey[key] = row
	}
	tombstones := map[string]bool{}
	if paths, err := s.opts.Workspaces.EvictedPaths(workspaceID); err == nil {
		for _, p := range paths {
			_, key := pathutil.Canonical(p)
			tombstones[key] = true
		}
	}
	errs, err := s.opts.Workspaces.Errors(workspaceID)
	if err != nil {
		return nil, err
	}

	out := make([]dto.CorpusDocumentStatus, 0, len(files))
	for _, f := range files {
		doc := dto.CorpusDocumentStatus{
			ID:     pathutil.DocID(f.path),
			Path:   f.path,
			Status: "pending",
		}
		switch {
		case tombstones[f.pathKey]:
			doc.Status = "evicted"
		case errs[f.pathKey] != "":
			doc.Status = "error"
			doc.Error = errs[f.pathKey]
		default:
			if row, ok := byKey[f.pathKey]; ok {
				doc.ChunkCount = row.ChunkCount
				doc.IndexedAt = row.IndexedAt
				diskHash, err := s.diskHash(ctx, f.path)
				switch {
				case err != nil:
					doc.Status = "error"
					doc.Error = err.Error()
				case diskHash != row.ContentHash:
					doc.Status = "stale"
				default:
					doc.Status = "indexed"
				}
			}
		}
		out = append(out, doc)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// diskHash reads a bounded corpus file and returns its content hash.
func (s *service) diskHash(ctx context.Context, path string) (string, error) {
	raw, err := s.opts.Filesystem.Read(ctx, path, readCap)
	if err != nil {
		return "", err
	}
	return pathutil.Hash(string(raw)), nil
}

// walk enumerates the scope's in-scope files: canonicalized, deduped across
// roots, hidden directories/files excluded, include/exclude globs applied
// (ADR-0049 §3). Files are never opened for writing.
func (s *service) walk(scope dto.CorpusScope) ([]scopeFile, error) {
	seen := map[string]bool{}
	var out []scopeFile
	add := func(path, rel string) {
		canonical, key := pathutil.Canonical(path)
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, scopeFile{path: canonical, pathKey: key, rel: rel})
	}

	for _, root := range scope.Roots {
		canonicalRoot, _ := pathutil.Canonical(root)
		info, err := os.Stat(canonicalRoot)
		if err != nil {
			continue // missing root: nothing to enumerate
		}
		if !info.IsDir() {
			if !info.Mode().IsRegular() {
				continue
			}
			rel := filepath.Base(canonicalRoot)
			if !hiddenPath(rel) && inScope(scope, rel) {
				add(canonicalRoot, rel)
			}
			continue
		}
		_ = filepath.WalkDir(canonicalRoot, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			name := d.Name()
			if d.IsDir() {
				if p != canonicalRoot && strings.HasPrefix(name, ".") {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasPrefix(name, ".") {
				return nil
			}
			rel, err := filepath.Rel(canonicalRoot, p)
			if err != nil {
				return nil
			}
			rel = filepath.ToSlash(rel)
			if inScope(scope, rel) {
				add(p, rel)
			}
			return nil
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out, nil
}

// inScope applies include/exclude globs to a root-relative slash path.
func inScope(scope dto.CorpusScope, rel string) bool {
	if !glob.MatchAny(scope.Include, rel) {
		return false
	}
	return !glob.MatchAny(scope.Exclude, rel)
}

// hiddenPath reports whether any path segment starts with a dot.
func hiddenPath(rel string) bool {
	for _, seg := range strings.Split(filepath.ToSlash(rel), "/") {
		if strings.HasPrefix(seg, ".") {
			return true
		}
	}
	return false
}

// DocHook decorates a Document store so successful Open/Commit/SaveTree
// boundaries enqueue a corpus re-index when the canonical path is in a
// workspace's corpus (ADR-0049 §4 lifecycle) and emit a `document` feed event
// (ADR-0052 §4). It never blocks the write.
type DocHook struct {
	document.Interface
	Corpus Interface
	// Bus, when set, receives `document` feed events (external-change/commit,
	// ADR-0052 §4). A nil bus disables emission.
	Bus Emitter
}

// Open wraps the document open and notifies the corpus on success. A re-read
// caused by an external disk change is emitted as a `document` event.
func (h *DocHook) Open(path string) (dto.OpenResult, error) {
	res, err := h.Interface.Open(path)
	if err == nil {
		h.Corpus.NotifyChanged(res.Path)
		if res.ExternalChange {
			h.emitDocument("external-change", res.DocumentID, res.Path)
		}
	}
	return res, err
}

// Commit wraps the commit (write-through) and notifies on success, emitting a
// `document` commit event.
func (h *DocHook) Commit(documentID string, opts dto.CommitOptions) (dto.WriteResult, error) {
	res, err := h.Interface.Commit(documentID, opts)
	if err == nil && res.Path != "" {
		h.Corpus.NotifyChanged(res.Path)
		h.emitDocument("commit", documentID, res.Path)
	}
	return res, err
}

// SaveTree wraps the manual save; only a write-through touches disk, so only
// that boundary notifies and emits.
func (h *DocHook) SaveTree(documentID string, tree []dto.BlockWrite, opts dto.SaveOptions) (dto.WriteResult, error) {
	res, err := h.Interface.SaveTree(documentID, tree, opts)
	if err == nil && res.WrittenThrough && res.Path != "" {
		h.Corpus.NotifyChanged(res.Path)
		h.emitDocument("commit", documentID, res.Path)
	}
	return res, err
}

// emitDocument publishes one global `document` feed event (ADR-0052 §4).
func (h *DocHook) emitDocument(kind, documentID, path string) {
	if h.Bus == nil {
		return
	}
	data, err := json.Marshal(dto.DocumentEventPayload{Kind: kind, DocumentID: documentID, Path: path})
	if err != nil {
		return
	}
	h.Bus.Emit(dto.Event{Type: dto.EventDocument, Data: data})
}

var _ document.Interface = (*DocHook)(nil)
var _ Interface = (*service)(nil)
