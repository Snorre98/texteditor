// Package apiserver is the thin API-server adapter (ADR-0017 §4/§5, ADR-0031).
// It implements the ogen-generated Handler + RawHandler over the engine's sealed
// module interfaces, mapping each REST route to its module and hand-framing the
// `/turn` SSE stream. The generated genapi package owns routing, request decode,
// validation, and otel; this package owns only the seam to the engine modules.
package apiserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"

	"texteditor/internal/corpus"
	"texteditor/internal/document"
	"texteditor/internal/filesystem"
	"texteditor/internal/fleet"
	"texteditor/internal/genapi"
	"texteditor/internal/loop"
	"texteditor/internal/mode"
	"texteditor/internal/pathutil"
	"texteditor/internal/session"
	"texteditor/internal/shard"
	"texteditor/internal/tool"
	"texteditor/internal/workspace"
	"texteditor/shared/dto"
)

// Deps holds the injected engine modules the server adapts (composition root
// wires these to the real gateways/stores). BaseURL is the engine's own bound
// base URL, advertised via /health for dynamic-port discovery (ADR-0021 §1).
// CORSOrigins is the explicit origin allowlist (ADR-0037); empty = CORS disabled.
type Deps struct {
	Fleet       fleet.Interface
	Modes       mode.Interface
	Tools       tool.Registry
	Doc         document.Interface
	Loop        loop.Interface
	Filesystem  filesystem.Interface
	Workspaces  workspace.Interface // registry: workspace + session routing
	Shards      shard.Resolver      // workspace-scoped sessions/meter/index
	Corpus      corpus.Interface    // corpus scope/status/index/evict (ADR-0049 §4)
	BaseURL     string
	CORSOrigins []string
}

// Server wraps the generated ogen server with a handler adapter. ServeHTTP
// applies the CORS policy (when enabled) then delegates to the generated router.
type Server struct {
	srv  *genapi.Server
	d    Deps
	cors *corsPolicy
}

// New builds the API server. bus must implement Emit (for SSE fan-out via
// Subscribe); when streaming is not needed a nil-safe no-op is acceptable for
// non-stream routes, but /turn requires the real bus.
func New(d Deps, bus EventSource) (*Server, error) {
	h := &handler{d: d, bus: bus}
	srv, err := genapi.NewServer(h, h)
	if err != nil {
		return nil, err
	}
	return &Server{srv: srv, d: d, cors: newCORSPolicy(d.CORSOrigins)}, nil
}

// EventSource is the sealed subset of the event bus the /turn handler needs:
// subscribe with a filter. The loop/meter already hold an Emit subset.
type EventSource interface {
	Subscribe(filter func(dto.Event) bool) <-chan dto.Event
}

type handler struct {
	d   Deps
	bus EventSource
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.cors != nil && s.cors.apply(w, r) {
		return
	}
	s.srv.ServeHTTP(w, r)
}

// ------------------------- typed non-streaming surface -------------------------

func (h *handler) GetHealth(ctx context.Context) (*genapi.Health, error) {
	res := &genapi.Health{Status: genapi.HealthStatusOk}
	if h.d.BaseURL != "" {
		res.BaseUrl = genapi.NewOptString(h.d.BaseURL)
	}
	return res, nil
}

func (h *handler) ListModels(ctx context.Context) ([]genapi.Model, error) {
	models, byState, unreachable, err := h.fleetSnapshot()
	if err != nil || unreachable {
		// /models keeps its hard-fail semantics (ADR-0040 §3); /fleet is the
		// labeled observability surface that renders a daemon outage.
		if err == nil {
			err = fleet.ErrDaemonUnreachable
		}
		return nil, err
	}
	out := make([]genapi.Model, 0, len(models))
	for _, m := range models {
		om := modelToGen(m)
		if st, ok := byState[m.Name]; ok {
			om.LiveState = genapi.NewOptModelLiveState(genapi.ModelLiveState(st))
		}
		out = append(out, om)
	}
	return out, nil
}

// GetFleet backs GET /fleet (ADR-0040): one observability read joining the
// model projection with the batch `status/all` states. A daemon outage is
// data, not an error — `control: unreachable` with the last-known projection
// and every state forced to `unknown` (ADR-0040 §3, failure-semantics §6).
func (h *handler) GetFleet(ctx context.Context) (*genapi.FleetState, error) {
	models, byState, unreachable, err := h.fleetSnapshot()
	if err != nil {
		return nil, err
	}
	control := genapi.FleetStateControlUp
	if unreachable {
		control = genapi.FleetStateControlUnreachable
	}
	out := make([]genapi.FleetModel, 0, len(models))
	for _, m := range models {
		st, ok := byState[m.Name]
		if !ok {
			st = dto.LiveUnknown
		}
		out = append(out, fleetModelToGen(m, st))
	}
	return &genapi.FleetState{Control: control, Models: out}, nil
}

// fleetSnapshot reads the fleet projection + batch states in two daemon calls
// (ADR-0040 §2). It returns `unreachable` when the control plane is down —
// carrying the last-good projection from the gateway's cache — and propagates
// any other error.
func (h *handler) fleetSnapshot() (models []dto.Model, byState map[string]dto.LiveState, unreachable bool, err error) {
	models, modelsErr := h.d.Fleet.ListModels()
	states, statesErr := h.d.Fleet.ListStatus()

	unreachable = errors.Is(modelsErr, fleet.ErrDaemonUnreachable) ||
		errors.Is(statesErr, fleet.ErrDaemonUnreachable)
	if !unreachable {
		if modelsErr != nil {
			return nil, nil, false, modelsErr
		}
		if statesErr != nil {
			return nil, nil, false, statesErr
		}
	}

	byState = map[string]dto.LiveState{}
	if unreachable {
		for _, m := range models {
			byState[m.Name] = dto.LiveUnknown
		}
	} else {
		for _, st := range states {
			byState[st.Name] = st.State
		}
	}
	return models, byState, unreachable, nil
}

func (h *handler) GetModelStatus(ctx context.Context, p genapi.GetModelStatusParams) (*genapi.LiveStateResponse, error) {
	st, err := h.d.Fleet.Status(p.Name)
	if err != nil {
		return nil, err
	}
	return &genapi.LiveStateResponse{
		Name:  p.Name,
		State: genapi.LiveStateResponseState(st),
	}, nil
}

func (h *handler) StartModel(ctx context.Context, p genapi.StartModelParams) (*genapi.LiveStateResponse, error) {
	if err := h.d.Fleet.Start(p.Name); err != nil {
		return nil, err
	}
	return &genapi.LiveStateResponse{Name: p.Name, State: genapi.LiveStateResponseStateUp}, nil
}

func (h *handler) StopModel(ctx context.Context, p genapi.StopModelParams) (*genapi.LiveStateResponse, error) {
	if err := h.d.Fleet.Stop(p.Name); err != nil {
		return nil, err
	}
	return &genapi.LiveStateResponse{Name: p.Name, State: genapi.LiveStateResponseStateDown}, nil
}

func (h *handler) ProvisionModel(ctx context.Context, p genapi.ProvisionModelParams) (*genapi.ProvisionResponse, error) {
	id, err := h.d.Fleet.Provision(ctx, p.Name)
	if err != nil {
		return nil, err
	}
	return &genapi.ProvisionResponse{ProvisionID: id}, nil
}

func (h *handler) ListModes(ctx context.Context) ([]genapi.Mode, error) {
	modes := h.d.Modes.List()
	out := make([]genapi.Mode, 0, len(modes))
	for _, m := range modes {
		out = append(out, modeToGen(m))
	}
	return out, nil
}

func (h *handler) ListTools(ctx context.Context) ([]genapi.ToolDef, error) {
	defs := h.d.Tools.List()
	out := make([]genapi.ToolDef, 0, len(defs))
	for _, d := range defs {
		out = append(out, toolDefToGen(d))
	}
	return out, nil
}

// ListDirectory backs GET /directories (ADR-0035 §2): a shallow, engine-served
// directory listing through the Filesystem leaf, bounded by ALLOWED_ROOTS
// (ADR-0049 §6). List failures project as not-found / not-a-directory /
// path-outside-allowed-roots (interface.md §9b).
func (h *handler) ListDirectory(ctx context.Context, p genapi.ListDirectoryParams) (genapi.ListDirectoryRes, error) {
	entries, err := h.d.Filesystem.List(ctx, p.Path)
	if err != nil {
		if errors.Is(err, filesystem.ErrPathOutsideAllowedRoots) {
			return h.outsideRoots(p.Path), nil
		}
		return nil, err
	}
	out := make([]genapi.Entry, 0, len(entries))
	for _, e := range entries {
		out = append(out, genapi.Entry{Name: e.Name, Path: e.Path, IsDir: e.IsDir})
	}
	return &genapi.DirectoryListing{Path: p.Path, Entries: out}, nil
}

// outsideRoots renders the typed ALLOWED_ROOTS refusal (ADR-0049 §6): never
// silent, always carrying the allowlist so a client can explain the boundary.
func (h *handler) outsideRoots(path string) *genapi.PathOutsideAllowedRoots {
	return &genapi.PathOutsideAllowedRoots{
		Error:        genapi.PathOutsideAllowedRootsErrorPathOutsideAllowedRoots,
		Path:         path,
		AllowedRoots: h.d.Filesystem.AllowedRoots(),
	}
}

// ------------------------- workspaces + corpus (ADR-0049) -------------------------

func (h *handler) ListWorkspaces(ctx context.Context) ([]genapi.Workspace, error) {
	ws, err := h.d.Workspaces.List()
	if err != nil {
		return nil, err
	}
	out := make([]genapi.Workspace, 0, len(ws))
	for _, w := range ws {
		out = append(out, workspaceToGen(w))
	}
	return out, nil
}

func (h *handler) CreateWorkspace(ctx context.Context, req *genapi.CreateWorkspaceRequest) (*genapi.Workspace, error) {
	ws, err := h.d.Workspaces.ResolveOrCreate(req.Root)
	if err != nil {
		return nil, err
	}
	o := workspaceToGen(ws)
	return &o, nil
}

func (h *handler) GetWorkspace(ctx context.Context, p genapi.GetWorkspaceParams) (*genapi.Workspace, error) {
	ws, err := h.d.Workspaces.Get(p.ID)
	if err != nil {
		return nil, err
	}
	o := workspaceToGen(ws)
	return &o, nil
}

func (h *handler) GetCorpus(ctx context.Context, p genapi.GetCorpusParams) (*genapi.CorpusState, error) {
	state, err := h.d.Corpus.Get(ctx, p.WorkspaceId)
	if err != nil {
		return nil, err
	}
	o := corpusStateToGen(state)
	return &o, nil
}

func (h *handler) PutCorpus(ctx context.Context, req *genapi.PutCorpusRequest) (genapi.PutCorpusRes, error) {
	scope := dto.CorpusScope{Roots: req.Roots, Include: req.Include, Exclude: req.Exclude}
	state, err := h.d.Corpus.SetScope(ctx, req.WorkspaceId, scope)
	if err != nil {
		if errors.Is(err, filesystem.ErrPathOutsideAllowedRoots) {
			return h.outsideRoots(req.WorkspaceId), nil
		}
		return nil, err
	}
	o := corpusStateToGen(state)
	return &o, nil
}

func (h *handler) IndexCorpus(ctx context.Context, req *genapi.CorpusIndexRequest) (*genapi.CorpusJob, error) {
	job, err := h.d.Corpus.Index(ctx, req.WorkspaceId)
	if err != nil {
		return nil, err
	}
	o := corpusJobToGen(job)
	return &o, nil
}

func (h *handler) EvictCorpusDocument(ctx context.Context, p genapi.EvictCorpusDocumentParams) (genapi.EvictCorpusDocumentRes, error) {
	if err := h.d.Corpus.Evict(ctx, p.WorkspaceId, p.ID); err != nil {
		if errors.Is(err, filesystem.ErrPathOutsideAllowedRoots) {
			return h.outsideRoots(p.ID), nil
		}
		return nil, err
	}
	return &genapi.EvictCorpusDocumentNoContent{}, nil
}

func workspaceToGen(w dto.Workspace) genapi.Workspace {
	return genapi.Workspace{
		ID:        w.ID,
		Root:      w.Root,
		Name:      w.Name,
		CreatedAt: w.CreatedAt,
		UpdatedAt: w.UpdatedAt,
	}
}

func corpusStateToGen(state dto.CorpusState) genapi.CorpusState {
	out := genapi.CorpusState{
		WorkspaceId: state.WorkspaceID,
		Roots:       state.Roots,
		Include:     state.Include,
		Exclude:     state.Exclude,
		Documents:   make([]genapi.CorpusDocument, 0, len(state.Documents)),
	}
	for _, d := range state.Documents {
		doc := genapi.CorpusDocument{
			ID:     d.ID,
			Path:   d.Path,
			Status: genapi.CorpusDocumentStatus(d.Status),
		}
		if d.ChunkCount != 0 {
			doc.ChunkCount = genapi.NewOptInt(d.ChunkCount)
		}
		if d.IndexedAt != 0 {
			doc.IndexedAt = genapi.NewOptInt64(d.IndexedAt)
		}
		if d.Error != "" {
			doc.Error = genapi.NewOptString(d.Error)
		}
		out.Documents = append(out.Documents, doc)
	}
	if state.Job != nil {
		out.Job = genapi.NewOptCorpusJob(corpusJobToGen(*state.Job))
	}
	return out
}

func corpusJobToGen(j dto.CorpusJob) genapi.CorpusJob {
	out := genapi.CorpusJob{
		ID:          j.ID,
		WorkspaceId: j.WorkspaceID,
		Kind:        genapi.CorpusJobKind(j.Kind),
		State:       genapi.CorpusJobState(j.State),
		Total:       j.Total,
		Completed:   j.Completed,
	}
	if j.Error != "" {
		out.Error = genapi.NewOptString(j.Error)
	}
	if j.StartedAt != 0 {
		out.StartedAt = genapi.NewOptInt64(j.StartedAt)
	}
	if j.FinishedAt != 0 {
		out.FinishedAt = genapi.NewOptInt64(j.FinishedAt)
	}
	return out
}

func (h *handler) OpenDocument(ctx context.Context, req *genapi.OpenDocumentRequest) (*genapi.Document, error) {
	res, err := h.d.Doc.Open(req.Path)
	if err != nil {
		return nil, err
	}
	blocks, _ := h.d.Doc.Blocks(res.DocumentID)
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
	return out, nil
}

func (h *handler) GetBlocks(ctx context.Context, p genapi.GetBlocksParams) ([]genapi.Block, error) {
	blocks, err := h.d.Doc.Blocks(p.ID)
	if err != nil {
		return nil, err
	}
	out := make([]genapi.Block, 0, len(blocks))
	for _, b := range blocks {
		out = append(out, blockToGen(b))
	}
	return out, nil
}

func (h *handler) ApplyEdit(ctx context.Context, req *genapi.BlockEdit, p genapi.ApplyEditParams) (*genapi.Revision, error) {
	edit := dto.BlockEdit{BlockID: req.BlockId, Text: req.Text}
	for _, g := range req.Guards {
		edit.Guards = append(edit.Guards, dto.Guard{BlockID: g.BlockId, Hash: g.Hash})
	}
	rev, err := h.d.Doc.ApplyEdit(ctx, p.ID, edit)
	if err != nil {
		return nil, err
	}
	return revisionToGen(rev), nil
}

// CommitDocument backs POST /documents/{id}/commits (ADR-0047 §1): accept the
// staged candidates, derive the commit message, mirror to the opened file, and
// report the write-through. An externally changed file is refused with the typed
// 409 (unless the client explicitly overwrites); an empty accept creates no
// commit and no write.
func (h *handler) CommitDocument(ctx context.Context, req genapi.OptCommitRequest, p genapi.CommitDocumentParams) (genapi.CommitDocumentRes, error) {
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

// SaveDocument backs PUT /documents/{id}/tree (ADR-0038): the manual-edit whole-
// tree snapshot. The engine reconciles, mints IDs for new blocks, formats, and
// commits `autosave @ <ts>` iff changed; writeThrough mirrors the canonical
// markdown back to the opened file (ADR-0039). Per ADR-0047 §4 a no-op save
// still re-syncs a stale disk, and an external change is a typed 409 (unless the
// client explicitly overwrites).
func (h *handler) SaveDocument(ctx context.Context, req *genapi.SaveTreeRequest, p genapi.SaveDocumentParams) (genapi.SaveDocumentRes, error) {
	tree := make([]dto.BlockWrite, 0, len(req.Blocks))
	for _, b := range req.Blocks {
		bw := dto.BlockWrite{
			Kind: dto.BlockKind(b.Kind),
			Text: b.Text,
		}
		if v, ok := b.ID.Get(); ok {
			bw.ID = &v
		}
		if v, ok := b.ParentId.Get(); ok {
			bw.ParentID = &v
		}
		tree = append(tree, bw)
	}
	opts := dto.SaveOptions{}
	if v, ok := req.WriteThrough.Get(); ok {
		opts.WriteThrough = v
	}
	if v, ok := req.Overwrite.Get(); ok {
		opts.Overwrite = v
	}
	res, err := h.d.Doc.SaveTree(p.ID, tree, opts)
	if err != nil {
		if conflict, ok := asFileChanged(err); ok {
			return conflict, nil
		}
		return nil, err
	}
	return writeResultToGen(res), nil
}

// asFileChanged maps the store's typed write-through conflict to the generated
// 409 response carrying the current on-disk hash (ADR-0047 §3).
func asFileChanged(err error) (*genapi.FileChangedExternally, bool) {
	var fce *document.FileChangedExternallyError
	if !errors.As(err, &fce) {
		return nil, false
	}
	return &genapi.FileChangedExternally{
		Error:       genapi.FileChangedExternallyErrorFileChangedExternally,
		Path:        fce.Path,
		CurrentHash: fce.CurrentHash,
	}, true
}

func (h *handler) GetHistory(ctx context.Context, p genapi.GetHistoryParams) ([]genapi.Revision, error) {
	revs, err := h.d.Doc.History(p.ID)
	if err != nil {
		return nil, err
	}
	out := make([]genapi.Revision, 0, len(revs))
	for _, r := range revs {
		out = append(out, *revisionToGen(r))
	}
	return out, nil
}

func (h *handler) GetDiff(ctx context.Context, p genapi.GetDiffParams) ([]genapi.WordEdit, error) {
	edits, err := h.d.Doc.Diff(p.ID, p.Base, p.Rev)
	if err != nil {
		return nil, err
	}
	out := make([]genapi.WordEdit, 0, len(edits))
	for _, e := range edits {
		out = append(out, genapi.WordEdit{
			BlockId:    e.BlockID,
			Insertions: e.Insertions,
			Deletions:  e.Deletions,
		})
	}
	return out, nil
}

func (h *handler) GetCandidates(ctx context.Context, p genapi.GetCandidatesParams) ([]genapi.Candidate, error) {
	cands, err := h.d.Doc.Candidates(p.ID, p.Bid)
	if err != nil {
		return nil, err
	}
	out := make([]genapi.Candidate, 0, len(cands))
	for _, c := range cands {
		out = append(out, genapi.Candidate{
			BlockId: genapi.NewOptString(c.BlockID),
			Text:    genapi.NewOptString(c.Text),
			BaseId:  genapi.NewOptString(c.BaseID),
		})
	}
	return out, nil
}

// resolveWorkspaceFor resolves the workspace shard for a session operation
// (ADR-0049 §5): an explicit workspaceId wins; otherwise the document's
// canonical path resolves to its containing workspace, or (create=true)
// resolve-or-creates one rooted at the document's parent directory — the
// fallback that keeps pre-workspace clients working.
func (h *handler) resolveWorkspaceFor(workspaceID, documentID string, create bool) (string, error) {
	if workspaceID != "" {
		if _, err := h.d.Workspaces.Get(workspaceID); err != nil {
			return "", err
		}
		return workspaceID, nil
	}
	if documentID == "" {
		return "", errors.New("workspace-or-document-required")
	}
	path, err := h.d.Doc.Path(documentID)
	if err != nil {
		return "", err
	}
	canonical, _ := pathutil.Canonical(path)
	if !create {
		if ws, ok, err := h.d.Workspaces.FindContaining(canonical); err != nil {
			return "", err
		} else if ok {
			return ws.ID, nil
		}
		return "", nil
	}
	ws, err := h.d.Workspaces.ResolveOrCreate(filepath.Dir(canonical))
	if err != nil {
		return "", err
	}
	return ws.ID, nil
}

// ListSessions backs GET /sessions: workspace-scoped, optionally filtered by
// document (ADR-0049 §5/§16).
func (h *handler) ListSessions(ctx context.Context, p genapi.ListSessionsParams) ([]genapi.Session, error) {
	workspaceID, err := h.resolveWorkspaceFor(p.WorkspaceId.Or(""), p.DocumentId.Or(""), false)
	if err != nil {
		return nil, err
	}
	if workspaceID == "" {
		return []genapi.Session{}, nil
	}
	lease, err := h.d.Shards.Services(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	defer lease.Release()

	var sess []dto.Session
	if docID := p.DocumentId.Or(""); docID != "" {
		sess, err = lease.Sessions.ListByDocument(docID)
	} else {
		sess, err = lease.Sessions.ListByWorkspace()
	}
	if err != nil {
		return nil, err
	}
	out := make([]genapi.Session, 0, len(sess))
	for _, s := range sess {
		out = append(out, sessionToGen(s, workspaceID))
	}
	return out, nil
}

func (h *handler) CreateSession(ctx context.Context, req *genapi.CreateSessionRequest) (*genapi.Session, error) {
	workspaceID, err := h.resolveWorkspaceFor(req.WorkspaceId.Or(""), req.DocumentId, true)
	if err != nil {
		return nil, err
	}
	lease, err := h.d.Shards.Services(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	defer lease.Release()

	var anchor *string
	if v, ok := req.AnchorBlockId.Get(); ok {
		anchor = &v
	}
	modeType := req.ModeType.Or("")
	s, err := lease.Sessions.Create(req.DocumentId, anchor, modeType)
	if err != nil {
		return nil, err
	}
	// Route the session id to its shard so message/meter reads resolve later.
	if err := h.d.Workspaces.RouteSession(s.ID, workspaceID); err != nil {
		return nil, err
	}
	o := sessionToGen(s, workspaceID)
	return &o, nil
}

func (h *handler) GetSessionMessages(ctx context.Context, p genapi.GetSessionMessagesParams) ([]genapi.Message, error) {
	workspaceID, ok, err := h.d.Workspaces.SessionWorkspace(p.ID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("session not found: %s", p.ID)
	}
	lease, err := h.d.Shards.Services(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	defer lease.Release()

	msgs, err := lease.Sessions.History(p.ID)
	if err != nil {
		return nil, err
	}
	out := make([]genapi.Message, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, genapi.Message{
			Role:    genapi.MessageRole(m.Role),
			Content: m.Content,
		})
	}
	return out, nil
}

// GetTurnContext backs GET /turns/{id}/context (ADR-0044 §4): resolve the
// turn's workspace shard through the registry's turn routing index, read the
// persisted snapshot, and return it. The snapshot is engine-owned data; an
// unknown turn is the typed 404.
func (h *handler) GetTurnContext(ctx context.Context, p genapi.GetTurnContextParams) (genapi.GetTurnContextRes, error) {
	workspaceID, _, ok, err := h.d.Workspaces.TurnRoute(p.ID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return notFound("turn", p.ID), nil
	}
	lease, err := h.d.Shards.Services(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	defer lease.Release()

	raw, err := lease.Sessions.TurnContext(p.ID)
	if errors.Is(err, session.ErrNotFound) {
		return notFound("turn", p.ID), nil
	}
	if err != nil {
		return nil, err
	}
	var snap genapi.ContextSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return nil, err
	}
	return &snap, nil
}

// GetSessionMeter backs GET /sessions/{id}/meter (ADR-0044 §4): resolve the
// session's workspace shard and aggregate its meter_events by component. An
// unknown session is the typed 404.
func (h *handler) GetSessionMeter(ctx context.Context, p genapi.GetSessionMeterParams) (genapi.GetSessionMeterRes, error) {
	workspaceID, ok, err := h.d.Workspaces.SessionWorkspace(p.ID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return notFound("session", p.ID), nil
	}
	lease, err := h.d.Shards.Services(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	defer lease.Release()
	if lease.Meter == nil {
		return nil, fmt.Errorf("meter-unavailable")
	}
	m, err := lease.Meter.SessionBreakdown(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	return sessionMeterToGen(m), nil
}

// notFound renders the typed routing refusal (api/openapi.yaml NotFound).
func notFound(resource, id string) *genapi.NotFound {
	return &genapi.NotFound{
		Error:    genapi.NotFoundErrorNotFound,
		Resource: genapi.NotFoundResource(resource),
		ID:       id,
	}
}

// sessionMeterToGen projects a dto.SessionMeter onto the wire schema.
func sessionMeterToGen(m dto.SessionMeter) *genapi.SessionMeter {
	out := &genapi.SessionMeter{
		SessionId:  m.SessionID,
		Components: make([]genapi.SessionMeterComponentsItem, 0, len(m.Components)),
		Total:      m.Total,
	}
	for _, c := range m.Components {
		item := genapi.SessionMeterComponentsItem{
			Component:        genapi.SessionMeterComponentsItemComponent(c.Component),
			PromptTokens:     c.PromptTokens,
			CompletionTokens: c.CompletionTokens,
		}
		if c.Approx {
			item.Approx = genapi.NewOptBool(true)
		}
		out.Components = append(out.Components, item)
	}
	return out
}

// StartTurn is the hand-framed `/turn` SSE handler (ADR-0031). It decodes the
// task (already done by ogen), starts the loop asynchronously to obtain a
// turnID, subscribes to the bus filtered to that turnID, and writes the SSE
// stream correlating one turn's events to exactly one client connection
// (ADR-0016 §3).
func (h *handler) StartTurn(ctx context.Context, req *genapi.Task, w http.ResponseWriter) error {
	if h.bus == nil {
		return fmt.Errorf("event bus not wired")
	}

	task := dto.Task{
		SessionID:   req.SessionId,
		ModeName:    req.ModeName,
		DocumentID:  req.DocumentId,
		WorkspaceID: req.WorkspaceId.Or(""),
		UserInput:   req.UserInput,
	}
	for _, m := range req.Mentions {
		task.Mentions = append(task.Mentions, dto.Mention{Path: m.Path})
	}
	if sel, ok := req.Selection.Get(); ok {
		if bid, ok := sel.BlockId.Get(); ok {
			task.Selection = &dto.Selection{BlockID: bid}
		}
	}
	if opts, ok := req.Options.Get(); ok {
		task.Options = &dto.TurnOptions{}
		if t, ok := opts.Temperature.Get(); ok {
			task.Options.Temperature = &t
		}
		if m, ok := opts.Model.Get(); ok {
			task.Options.Model = m
		}
	}

	// Run returns a turnID synchronously, then the turn proceeds async; subscribe
	// immediately so we capture every event from the start.
	turnID, err := h.d.Loop.Run(ctx, task)
	if err != nil {
		return err
	}

	stream := h.bus.Subscribe(func(e dto.Event) bool { return e.TurnID == turnID })

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// ogen wraps the ResponseWriter in a codeRecorder, so a direct http.Flusher
	// assertion fails; ResponseController unwraps the chain to the real Flusher.
	rc := http.NewResponseController(w)

	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, more := <-stream:
			if !more {
				return nil
			}
			if err := writeSSE(w, ev); err != nil {
				return err
			}
			if err := rc.Flush(); err != nil {
				return err
			}
			if ev.Type == "done" || ev.Type == "error" {
				return nil
			}
		}
	}
}

// writeSSE frames one event as an SSE message: `event: <type>` + `data: <json>`.
func writeSSE(w http.ResponseWriter, ev dto.Event) error {
	if _, err := fmt.Fprintf(w, "event: %s\n", ev.Type); err != nil {
		return err
	}
	data := ev.Data
	if len(data) == 0 {
		data = json.RawMessage(`{}`)
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
		return err
	}
	return nil
}

// ------------------------- DTO conversions -------------------------

func modelToGen(m dto.Model) genapi.Model {
	om := genapi.Model{
		Name:     m.Name,
		BaseUrl:  m.BaseURL,
		ModeTags: m.ModeTags,
	}
	om.Capabilities = genapi.NewOptCapabilities(genapi.Capabilities{
		ContextLength:        m.Capabilities.ContextLength,
		ThinkingMode:         m.Capabilities.ThinkingMode,
		SupportsSystemPrompt: m.Capabilities.SupportsSystemPrompt,
	})
	return om
}

func fleetModelToGen(m dto.Model, st dto.LiveState) genapi.FleetModel {
	om := genapi.FleetModel{
		Name:      m.Name,
		BaseUrl:   m.BaseURL,
		ModeTags:  m.ModeTags,
		LiveState: genapi.FleetModelLiveState(st),
	}
	om.Capabilities = genapi.NewOptCapabilities(genapi.Capabilities{
		ContextLength:        m.Capabilities.ContextLength,
		ThinkingMode:         m.Capabilities.ThinkingMode,
		SupportsSystemPrompt: m.Capabilities.SupportsSystemPrompt,
	})
	return om
}

func modeToGen(m dto.Mode) genapi.Mode {
	return genapi.Mode{
		Name:         m.Name,
		SystemPrompt: m.SystemPrompt,
		DefaultModel: m.DefaultModel,
	}
}

func toolDefToGen(d dto.ToolDef) genapi.ToolDef {
	return genapi.ToolDef{
		Name:        d.Name,
		Description: genapi.NewOptString(d.Description),
	}
}

func blockToGen(b dto.Block) genapi.Block {
	ob := genapi.Block{
		ID:       b.ID,
		Kind:     genapi.BlockKind(b.Kind),
		Position: b.Position,
		Text:     b.Text,
	}
	if b.ParentID != nil {
		ob.ParentId = genapi.NewOptString(*b.ParentID)
	}
	ob.Hash = genapi.NewOptString(b.Hash)
	return ob
}

func revisionToGen(r dto.Revision) *genapi.Revision {
	out := &genapi.Revision{
		ID:        genapi.NewOptString(r.ID),
		Message:   genapi.NewOptString(r.Message),
		Timestamp: genapi.NewOptInt64(r.Timestamp),
	}
	if r.WrittenThrough {
		out.WrittenThrough = genapi.NewOptBool(true)
	}
	if r.Path != "" {
		out.Path = genapi.NewOptString(r.Path)
	}
	return out
}

// writeResultToGen projects a write boundary's outcome (revision + write-through
// fields) onto the wire Revision (ADR-0047 §8).
func writeResultToGen(res dto.WriteResult) *genapi.Revision {
	rev := res.Revision
	rev.WrittenThrough = res.WrittenThrough
	rev.Path = res.Path
	return revisionToGen(rev)
}

func sessionToGen(s dto.Session, workspaceID string) genapi.Session {
	o := genapi.Session{
		ID:         s.ID,
		DocumentId: s.DocumentID,
	}
	if workspaceID != "" {
		o.WorkspaceId = genapi.NewOptString(workspaceID)
	}
	if s.AnchorBlockID != nil {
		o.AnchorBlockId = genapi.NewOptString(*s.AnchorBlockID)
	}
	o.ModeType = genapi.NewOptString(s.ModeType)
	o.Title = genapi.NewOptString(s.Title)
	if s.TokenBudget != nil {
		o.TokenBudget = genapi.NewOptInt(*s.TokenBudget)
	}
	o.CreatedAt = genapi.NewOptInt64(s.CreatedAt)
	o.UpdatedAt = genapi.NewOptInt64(s.UpdatedAt)
	return o
}
