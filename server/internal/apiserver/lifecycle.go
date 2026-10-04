package apiserver

// Engine-owned context lifecycle verbs (ADR-0052 §1–§3): one-verb bootstrap
// (POST /open), session open-or-resume (POST /documents/{id}/session), and the
// atomic approve write boundary (POST /documents/{id}/blocks/{bid}/accept).
// These reuse the existing engine primitives — Filesystem.List for the
// directory/file discrimination, Workspaces.ResolveOrCreate for routing, the
// Session store's create-or-resume + newest-first list, and the Document
// store's Commit (revalidate → format → commit → write-through) — so the client
// sends one verb and renders the result with no path arithmetic or sequencing.

import (
	"context"
	"errors"
	"path/filepath"

	"texteditor/internal/filesystem"
	"texteditor/internal/genapi"
	"texteditor/internal/pathutil"
	"texteditor/shared/dto"
)

// Open backs POST /open: resolve a path into a directory listing or a document
// context (document + blocks + workspace + session + modes) in one call.
func (h *handler) Open(ctx context.Context, req *genapi.OpenRequest) (genapi.OpenRes, error) {
	// Discriminate engine-side by probing a bounded directory listing: a
	// directory lists, a file does not (ErrNotADirectory). The typed refusals
	// are rendered, never swallowed.
	entries, err := h.d.Filesystem.List(ctx, req.Path)
	if err == nil {
		ws, werr := h.d.Workspaces.ResolveOrCreate(req.Path)
		if werr != nil {
			return nil, werr
		}
		listing := genapi.DirectoryListing{
			Path:    req.Path,
			Entries: make([]genapi.Entry, 0, len(entries)),
		}
		for _, e := range entries {
			listing.Entries = append(listing.Entries, genapi.Entry{Name: e.Name, Path: e.Path, IsDir: e.IsDir})
		}
		return &genapi.OpenResult{
			Kind:      genapi.OpenResultKindDirectory,
			Workspace: workspaceToGen(ws),
			Listing:   genapi.NewOptDirectoryListing(listing),
		}, nil
	}
	if errors.Is(err, filesystem.ErrPathOutsideAllowedRoots) {
		return h.outsideRoots(req.Path), nil
	}
	if errors.Is(err, filesystem.ErrNotFound) {
		return notFound("path", req.Path), nil
	}

	// A file (or any non-directory): open it as a document.
	document, derr := h.d.Doc.Open(req.Path)
	if derr != nil {
		return nil, derr
	}
	canonical, _ := pathutil.Canonical(document.Path)
	ws, werr := h.d.Workspaces.ResolveOrCreate(filepath.Dir(canonical))
	if werr != nil {
		return nil, werr
	}
	blocks, berr := h.d.Doc.Blocks(document.DocumentID)
	if berr != nil {
		return nil, berr
	}

	var anchor *string
	if v, ok := req.AnchorBlockId.Get(); ok {
		anchor = &v
	}
	sess, serr := h.openOrResumeSession(ctx, document.DocumentID, anchor, req.ModeType.Or(""), ws.ID)
	if serr != nil {
		return nil, serr
	}

	out := &genapi.OpenResult{
		Kind:      genapi.OpenResultKindDocument,
		Workspace: workspaceToGen(ws),
		Document:  genapi.NewOptDocument(*documentToGen(document, blocks)),
		Blocks:    blocksToGen(blocks),
		Session:   genapi.NewOptSession(sessionToGen(sess, ws.ID)),
		Modes:     modesToGen(h.d.Modes.List()),
	}
	return out, nil
}

// OpenDocumentSession backs POST /documents/{id}/session: return the session to
// use — anchor-keyed when an anchor is given, else the document's most recently
// updated session, creating one if none exists (ADR-0052 §2).
func (h *handler) OpenDocumentSession(ctx context.Context, req genapi.OptOpenSessionRequest, p genapi.OpenDocumentSessionParams) (*genapi.Session, error) {
	var anchor *string
	if req.IsSet() {
		if v, ok := req.Value.AnchorBlockId.Get(); ok {
			anchor = &v
		}
	}
	workspaceID, err := h.resolveWorkspaceFor("", p.ID, true)
	if err != nil {
		return nil, err
	}
	sess, err := h.openOrResumeSession(ctx, p.ID, anchor, "", workspaceID)
	if err != nil {
		return nil, err
	}
	o := sessionToGen(sess, workspaceID)
	return &o, nil
}

// AcceptBlock backs POST /documents/{id}/blocks/{bid}/accept: the approve write
// boundary as one server-side operation. The client supplies no candidate text;
// the engine commits the staged candidate(s), re-validating base hashes,
// formatting, committing, and mirroring to the opened file (ADR-0047, ADR-0052
// §3). An external change is the typed 409, never a silent clobber.
func (h *handler) AcceptBlock(ctx context.Context, req genapi.OptCommitRequest, p genapi.AcceptBlockParams) (genapi.AcceptBlockRes, error) {
	opts := dto.CommitOptions{}
	if req.IsSet() {
		if v, ok := req.Value.Overwrite.Get(); ok {
			opts.Overwrite = v
		}
	}
	res, err := h.d.Doc.Commit(p.ID, opts)
	if err != nil {
		if conflict, ok := asFileChanged(err); ok {
			return conflict, nil
		}
		return nil, err
	}
	return writeResultToGen(res), nil
}

// openOrResumeSession is the shared session-selection policy (ADR-0052 §2):
// anchor-keyed create-or-resume when an anchor is given, otherwise the newest
// session for the document, otherwise a fresh doc-level session. The created
// session is routed to its workspace shard so message/meter reads resolve.
func (h *handler) openOrResumeSession(ctx context.Context, documentID string, anchor *string, modeType, workspaceID string) (dto.Session, error) {
	lease, err := h.d.Shards.Services(ctx, workspaceID)
	if err != nil {
		return dto.Session{}, err
	}
	defer lease.Release()

	if anchor != nil {
		s, err := lease.Sessions.Create(documentID, anchor, modeType, "")
		if err != nil {
			return dto.Session{}, err
		}
		if err := h.d.Workspaces.RouteSession(s.ID, workspaceID); err != nil {
			return dto.Session{}, err
		}
		return s, nil
	}
	if sessions, err := lease.Sessions.ListByDocument(documentID); err != nil {
		return dto.Session{}, err
	} else if len(sessions) > 0 {
		return sessions[0], nil
	}
	s, err := lease.Sessions.Create(documentID, nil, modeType, "")
	if err != nil {
		return dto.Session{}, err
	}
	if err := h.d.Workspaces.RouteSession(s.ID, workspaceID); err != nil {
		return dto.Session{}, err
	}
	return s, nil
}

// documentToGen projects an open result + its block tree onto the wire Document.
// rootBlockId is the first block's id (the tree's root) when present.
func documentToGen(res dto.OpenResult, blocks []dto.Block) *genapi.Document {
	root := res.DocumentID
	if len(blocks) > 0 {
		root = blocks[0].ID
	}
	out := &genapi.Document{
		ID:          res.DocumentID,
		Path:        res.Path,
		RootBlockId: root,
	}
	if res.ExternalChange {
		out.ExternalChange = genapi.NewOptBool(true)
	}
	return out
}

// blocksToGen projects a block tree onto the wire Block array.
func blocksToGen(blocks []dto.Block) []genapi.Block {
	out := make([]genapi.Block, 0, len(blocks))
	for _, b := range blocks {
		out = append(out, blockToGen(b))
	}
	return out
}

// modesToGen projects the prompt presets onto the wire Mode array.
func modesToGen(modes []dto.Mode) []genapi.Mode {
	out := make([]genapi.Mode, 0, len(modes))
	for _, m := range modes {
		out = append(out, modeToGen(m))
	}
	return out
}
