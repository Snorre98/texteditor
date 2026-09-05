// Package apiserver is the thin API-server adapter (ADR-0017 §4/§5, ADR-0031).
// It implements the ogen-generated Handler + RawHandler over the engine's sealed
// module interfaces, mapping each REST route to its module and hand-framing the
// `/turn` SSE stream. The generated genapi package owns routing, request decode,
// validation, and otel; this package owns only the seam to the engine modules.
package apiserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"texteditor/internal/document"
	"texteditor/internal/fleet"
	"texteditor/internal/genapi"
	"texteditor/internal/loop"
	"texteditor/internal/mode"
	"texteditor/internal/session"
	"texteditor/internal/tool"
	"texteditor/shared/dto"
)

// Deps holds the injected engine modules the server adapts (composition root
// wires these to the real gateways/stores).
type Deps struct {
	Fleet    fleet.Interface
	Modes    mode.Interface
	Tools    tool.Registry
	Doc      document.Interface
	Sessions session.Interface
	Loop     loop.Interface
}

// Server wraps the generated ogen server with a handler adapter. ServeHTTP is
// delegated to the generated router.
type Server struct {
	srv *genapi.Server
	d   Deps
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
	return &Server{srv: srv, d: d}, nil
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
	s.srv.ServeHTTP(w, r)
}

// ------------------------- typed non-streaming surface -------------------------

func (h *handler) GetHealth(ctx context.Context) (*genapi.Health, error) {
	return &genapi.Health{Status: genapi.HealthStatusOk}, nil
}

func (h *handler) ListModels(ctx context.Context) ([]genapi.Model, error) {
	models, err := h.d.Fleet.ListModels()
	if err != nil {
		return nil, err
	}
	out := make([]genapi.Model, 0, len(models))
	for _, m := range models {
		om := modelToGen(m)
		// Best-effort live state; a down model still lists.
		if st, err := h.d.Fleet.Status(m.Name); err == nil {
			om.LiveState = genapi.NewOptModelLiveState(genapi.ModelLiveState(st))
		}
		out = append(out, om)
	}
	return out, nil
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

func (h *handler) OpenDocument(ctx context.Context, req *genapi.OpenDocumentRequest) (*genapi.Document, error) {
	id, err := h.d.Doc.Open(req.Path)
	if err != nil {
		return nil, err
	}
	blocks, _ := h.d.Doc.Blocks(id)
	root := id
	if len(blocks) > 0 {
		root = blocks[0].ID
	}
	return &genapi.Document{
		ID:          id,
		Path:        req.Path,
		RootBlockId: root,
	}, nil
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

func (h *handler) CommitDocument(ctx context.Context, p genapi.CommitDocumentParams) (*genapi.Revision, error) {
	if err := h.d.Doc.Commit(p.ID, "accepted"); err != nil {
		return nil, err
	}
	return &genapi.Revision{}, nil
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

func (h *handler) ListSessions(ctx context.Context, p genapi.ListSessionsParams) ([]genapi.Session, error) {
	sess, err := h.d.Sessions.ListByDocument(p.DocumentId)
	if err != nil {
		return nil, err
	}
	out := make([]genapi.Session, 0, len(sess))
	for _, s := range sess {
		out = append(out, sessionToGen(s))
	}
	return out, nil
}

func (h *handler) CreateSession(ctx context.Context, req *genapi.CreateSessionRequest) (*genapi.Session, error) {
	var anchor *string
	if v, ok := req.AnchorBlockId.Get(); ok {
		anchor = &v
	}
	modeType := req.ModeType.Or("")
	s, err := h.d.Sessions.Create(req.DocumentId, anchor, modeType)
	if err != nil {
		return nil, err
	}
	o := sessionToGen(s)
	return &o, nil
}

func (h *handler) GetSessionMessages(ctx context.Context, p genapi.GetSessionMessagesParams) ([]genapi.Message, error) {
	msgs, err := h.d.Sessions.History(p.ID)
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
		SessionID:  req.SessionId,
		ModeName:   req.ModeName,
		DocumentID: req.DocumentId,
		UserInput:  req.UserInput,
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

func modeToGen(m dto.Mode) genapi.Mode {
	om := genapi.Mode{
		Name:          m.Name,
		ToolAllowlist: m.ToolAllowlist,
	}
	om.SystemPrompt = genapi.NewOptString(m.SystemPrompt)
	om.DefaultModel = genapi.NewOptString(m.DefaultModel)
	om.Params = genapi.NewOptSamplingParams(genapi.SamplingParams{
		Temperature: genapi.NewOptFloat64(m.Params.Temperature),
		MaxTokens:   genapi.NewOptInt(m.Params.MaxTokens),
	})
	om.MaxSteps = genapi.NewOptInt(m.MaxSteps)
	om.Agentic = genapi.NewOptBool(m.Agentic)
	om.Kind = genapi.NewOptString(m.Kind)
	om.Preamble = genapi.NewOptString(m.Preamble)
	om.ToolCalling = genapi.NewOptModeToolCalling(genapi.ModeToolCalling(m.ToolCalling))
	return om
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
	return &genapi.Revision{
		ID:        genapi.NewOptString(r.ID),
		Message:   genapi.NewOptString(r.Message),
		Timestamp: genapi.NewOptInt64(r.Timestamp),
	}
}

func sessionToGen(s dto.Session) genapi.Session {
	o := genapi.Session{
		ID:         s.ID,
		DocumentId: s.DocumentID,
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
