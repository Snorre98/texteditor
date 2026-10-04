package loop

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"texteditor/internal/locate"
	"texteditor/internal/shard"
	"texteditor/shared/dto"
)

// locateInstruction is the deterministic, replacement-only instruction appended
// to an anchored turn's assembled user input (ADR-0048 §5). It is never
// persisted: the session keeps the clean pasted chunk.
const locateInstruction = "Rewrite only the anchored block; return only its replacement text."

// ErrNoPendingLocate is the typed refusal (mapped to HTTP 409) when a locate
// picker answer arrives for a turn that is not waiting on one (ADR-0048 §4).
var ErrNoPendingLocate = errors.New("no-pending-locate: this turn is not waiting on a locate picker")

// locatePickerTimeout bounds the ambiguity picker wait (ADR-0048 §4). It is a
// package var so tests can inject a short timeout.
var locatePickerTimeout = 120 * time.Second

// anchor is the internal block-scoped turn target set by a resolved `/locate`
// (ADR-0048 §5): the block id, its file path, the base-hash guard, and the
// neighbor-window text injected as context.
type anchor struct {
	blockID  string
	path     string
	baseHash string
	context  string
}

// pendingLocate is one waiting turn's open picker.
type pendingLocate struct {
	result dto.LocateResult
	choice chan dto.LocateChoice
}

// parseLocateCommand recognizes a leading `/locate` line (optional surrounding
// whitespace) and returns the effective user input (the stripped pasted chunk),
// the chunk to locate, and whether the command was present. `/locate` with no
// following text yields an empty effective input and an empty chunk, degrading
// to plain chat with a not-found label (ADR-0048 §1).
func parseLocateCommand(input string) (effective, chunk string, ok bool) {
	first := input
	rest := ""
	if i := strings.IndexByte(input, '\n'); i >= 0 {
		first = input[:i]
		rest = input[i+1:]
	}
	if strings.TrimSpace(first) != "/locate" {
		return input, "", false
	}
	chunk = strings.TrimSpace(rest)
	return chunk, chunk, true
}

// locateTurn resolves the chunk, emits the `locate` event, and waits for the
// ambiguity picker when the match is fuzzy (ADR-0048 §4). It returns the anchor
// (nil unless a resolved match is the open document) and the result JSON for the
// snapshot. No model call and no edit happen before the choice.
func (l *loop) locateTurn(ctx context.Context, turnID string, task dto.Task, svc *shard.Services, chunk string) (*anchor, json.RawMessage) {
	res := dto.LocateResult{Status: dto.LocateStatusNotFound}
	resolver := l.d.Locate
	if resolver == nil && l.d.Doc != nil && l.d.Filesystem != nil && svc != nil && svc.Retriever != nil {
		// Production path: the resolver searches the open document (Document
		// store) then the turn's workspace index (this shard's Retriever), and
		// reads corpus files through the bounded Filesystem for staleness.
		resolver = locate.New(l.d.Doc, svc.Retriever, l.d.Filesystem)
	}
	if resolver != nil && svc != nil && svc.Retriever != nil && chunk != "" {
		r, err := resolver.Resolve(ctx, locate.Request{Chunk: chunk, DocumentID: task.DocumentID})
		if err == nil {
			res = r
		}
	}

	if res.Status == dto.LocateStatusAmbiguous {
		l.registerPending(turnID, res)
		l.emitLocate(turnID, res)
		choice, ok := l.waitForChoice(ctx, turnID)
		l.clearPending(turnID)
		if ok {
			res = resultForChoice(res, choice)
		} else {
			res = dto.LocateResult{Status: dto.LocateStatusNotFound, Context: "locate-cancelled"}
		}
	} else {
		l.emitLocate(turnID, res)
	}

	raw, _ := json.Marshal(res)
	if res.Status == dto.LocateStatusResolved {
		if a := l.buildAnchor(task.DocumentID, res.DocumentID, res.BlockID, res.Path); a != nil {
			return a, raw
		}
	}
	return nil, raw
}

// buildAnchor constructs the internal anchor from a resolved match, fetching the
// open document's blocks for the guard hash and neighbor context. A match in a
// different document (or a corpus file with no block id) is not anchorable here:
// a wrong anchor is worse than no anchor (ADR-0048).
func (l *loop) buildAnchor(openDocumentID, matchDocumentID, blockID, path string) *anchor {
	if openDocumentID == "" || blockID == "" || matchDocumentID != openDocumentID {
		return nil
	}
	blocks, err := l.d.Doc.Blocks(openDocumentID)
	if err != nil {
		return nil
	}
	idx := -1
	for i, b := range blocks {
		if b.ID == blockID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil
	}
	start := idx - locate.NeighborWindow
	if start < 0 {
		start = 0
	}
	end := idx + locate.NeighborWindow
	if end >= len(blocks) {
		end = len(blocks) - 1
	}
	parts := make([]string, 0, end-start+1)
	for _, b := range blocks[start : end+1] {
		if b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return &anchor{
		blockID:  blockID,
		path:     path,
		baseHash: blocks[idx].Hash,
		context:  strings.Join(parts, "\n\n"),
	}
}

// resultForChoice turns the picker's answer into a resolved result. An unknown
// key (or cancel) degrades to not-found with a label.
func resultForChoice(res dto.LocateResult, choice dto.LocateChoice) dto.LocateResult {
	if choice.Cancel {
		return dto.LocateResult{Status: dto.LocateStatusNotFound, Context: "locate-cancelled"}
	}
	for _, c := range res.Candidates {
		if c.ChunkKey == choice.ChunkKey || c.BlockID == choice.ChunkKey {
			return dto.LocateResult{
				Status:     dto.LocateStatusResolved,
				MatchType:  res.MatchType,
				Confidence: c.Score,
				DocumentID: c.DocumentID,
				Path:       c.Path,
				BlockID:    c.BlockID,
				ChunkKey:   c.ChunkKey,
				Stale:      c.Stale,
			}
		}
	}
	return dto.LocateResult{Status: dto.LocateStatusNotFound, Context: "locate-choice-not-found"}
}

// registerPending opens a picker for a waiting turn.
func (l *loop) registerPending(turnID string, res dto.LocateResult) {
	l.pendingMu.Lock()
	l.pending[turnID] = &pendingLocate{result: res, choice: make(chan dto.LocateChoice, 1)}
	l.pendingMu.Unlock()
}

// clearPending removes a turn's picker.
func (l *loop) clearPending(turnID string) {
	l.pendingMu.Lock()
	delete(l.pending, turnID)
	l.pendingMu.Unlock()
}

// waitForChoice blocks until the picker is answered, the bounded timeout
// elapses, or the turn context is cancelled. It reports whether a choice was
// made (false = cancel/timeout → labeled fail-open to plain chat).
func (l *loop) waitForChoice(ctx context.Context, turnID string) (dto.LocateChoice, bool) {
	l.pendingMu.Lock()
	p := l.pending[turnID]
	l.pendingMu.Unlock()
	if p == nil {
		return dto.LocateChoice{Cancel: true}, false
	}
	timer := time.NewTimer(locatePickerTimeout)
	defer timer.Stop()
	select {
	case c := <-p.choice:
		if c.Cancel {
			return c, false
		}
		return c, true
	case <-timer.C:
		return dto.LocateChoice{Cancel: true}, false
	case <-ctx.Done():
		return dto.LocateChoice{Cancel: true}, false
	}
}

// ResolveLocate answers a waiting turn's picker (ADR-0048 §4). It is called by
// the API route POST /turns/{id}/locate; an unknown or already-answered turn is
// the typed ErrNoPendingLocate (HTTP 409).
func (l *loop) ResolveLocate(turnID string, choice dto.LocateChoice) error {
	l.pendingMu.Lock()
	p := l.pending[turnID]
	l.pendingMu.Unlock()
	if p == nil {
		return ErrNoPendingLocate
	}
	select {
	case p.choice <- choice:
		return nil
	default:
		return ErrNoPendingLocate
	}
}

// emitLocate emits the `locate` SSE event carrying the result JSON (ADR-0048
// §3). The event is emitted before assembly. The event copy carries the turnID
// so a client can answer the picker route; the snapshot record does not.
func (l *loop) emitLocate(turnID string, res dto.LocateResult) {
	res.TurnID = turnID
	data, _ := json.Marshal(res)
	l.emit(turnID, dto.Event{Type: "locate", Data: data})
}
