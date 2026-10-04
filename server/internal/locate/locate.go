package locate

import (
	"context"
	"sort"
	"strconv"
	"strings"

	"texteditor/internal/pathutil"
	"texteditor/shared/dto"
)

// Resolver is the sealed `/locate` service public API (ADR-0048 §2, ADR-0016).
// It is deterministic and token-free: no model call, no embedding call.
type Resolver interface {
	Resolve(ctx context.Context, req Request) (dto.LocateResult, error)
}

// Request is one `/locate` resolution: the pasted chunk and the currently open
// document (optional; when empty only the workspace corpus index is searched).
type Request struct {
	Chunk      string
	DocumentID string
}

// DocumentReader is the sealed open-document source (satisfied by the Document
// store): the block tree (text + guard hash) and the canonical path.
type DocumentReader interface {
	Blocks(documentID string) ([]dto.Block, error)
	Path(documentID string) (string, error)
}

// Searcher is the sealed workspace-index seam (satisfied by the Retriever):
// deterministic FTS-only text search plus per-file status for staleness.
type Searcher interface {
	SearchText(ctx context.Context, query string, limit int) ([]dto.Chunk, error)
	Status() ([]dto.IndexedDocument, error)
}

// FileReader is the sealed bounded file reader used to detect a stale indexed
// file (satisfied by the Filesystem leaf, so ALLOWED_ROOTS bounds the read).
type FileReader interface {
	Read(ctx context.Context, path string, maxBytes int) ([]byte, error)
}

// Constants (ADR-0048 pinned decisions; tuned in Phase F).
const (
	// FuzzyThreshold is the conservative minimum score a fuzzy match must reach.
	FuzzyThreshold = 0.85
	// MaxCandidates caps the ranked picker list.
	MaxCandidates = 5
	// NeighborWindow is how many blocks on each side of the anchor are injected.
	NeighborWindow = 1

	// searchLimit is how many FTS chunks are considered before ranking.
	searchLimit = 25
	// maxSpanBlocks bounds a multi-block exact window.
	maxSpanBlocks = 8
	// maxFileRead bounds a staleness read (4 MiB, matching the corpus cap).
	maxFileRead = 4 << 20
	// previewLen bounds a candidate's text preview.
	previewLen = 160
)

// resolver is the concrete Resolver.
type resolver struct {
	docs   DocumentReader
	search Searcher
	files  FileReader
}

// New returns a Resolver over the open-document, workspace-index, and bounded
// file-read seams.
func New(docs DocumentReader, search Searcher, files FileReader) Resolver {
	return &resolver{docs: docs, search: search, files: files}
}

// Resolve runs the deterministic search order: the open document's blocks, then
// the workspace corpus index. Exact normalized matches auto-resolve (a single
// one) or are ambiguous (several); fuzzy matches always require confirmation
// (ADR-0048 §4). Any index failure degrades to not-found (fail-open); locating
// never blocks the turn.
func (r *resolver) Resolve(ctx context.Context, req Request) (dto.LocateResult, error) {
	norm := Normalize(req.Chunk)
	if norm == "" {
		return dto.LocateResult{Status: dto.LocateStatusNotFound}, nil
	}

	var openBlocks []dto.Block
	var openPath string
	var exact []match
	var fuzzy []match

	if req.DocumentID != "" {
		if blocks, err := r.docs.Blocks(req.DocumentID); err == nil && len(blocks) > 0 {
			openBlocks = blocks
			if p, err := r.docs.Path(req.DocumentID); err == nil {
				openPath, _ = pathutil.Canonical(p)
			}
			ex, fz := matchEntries(blockEntries(blocks, req.DocumentID, openPath), norm)
			exact = append(exact, ex...)
			fuzzy = append(fuzzy, fz...)
		}
	}

	status := r.statusMap()
	if chunks, err := r.search.SearchText(ctx, req.Chunk, searchLimit); err == nil && len(chunks) > 0 {
		entries := chunkEntries(chunks, status)
		ex, fz := matchEntries(entries, norm)
		exact = append(exact, ex...)
		fuzzy = append(fuzzy, fz...)
	}

	exact = dedupe(exact)
	switch len(exact) {
	case 1:
		return r.finish(ctx, exact[0], req, openBlocks, openPath, status), nil
	default:
		if len(exact) > 1 {
			return r.ambiguous(ctx, exact, status), nil
		}
	}

	fuzzy = filterThreshold(fuzzy, FuzzyThreshold)
	fuzzy = dedupe(fuzzy)
	if len(fuzzy) > 0 {
		return r.ambiguous(ctx, fuzzy, status), nil
	}
	return dto.LocateResult{Status: dto.LocateStatusNotFound}, nil
}

// entry is one candidate unit: an open-document block or an indexed chunk.
type entry struct {
	documentID string
	path       string
	blockID    string
	chunkKey   string
	text       string
	norm       string
	hash       string
	index      int // order within its source, for span windows
}

// key is the stable candidate identity (the picker choice key).
func (e entry) key() string {
	if e.chunkKey != "" {
		return e.chunkKey
	}
	if e.blockID != "" {
		return e.blockID
	}
	return e.path
}

// match is one exact or fuzzy candidate plus its span (multi-block only).
type match struct {
	cand dto.LocateCandidate
	span []string
}

// matchEntries finds exact single/window matches and fuzzy single matches among
// one source's ordered entries.
func matchEntries(entries []entry, norm string) (exact, fuzzy []match) {
	if len(entries) == 0 {
		return nil, nil
	}

	// Exact: per-block hash fast path, then a sliding multi-block window.
	byHash := map[string][]entry{}
	for _, e := range entries {
		byHash[e.hash] = append(byHash[e.hash], e)
	}
	for _, e := range byHash[NormalizedHash(norm)] {
		if e.norm == norm {
			exact = append(exact, match{cand: candidate(e, 1.0), span: []string{anchorOf(e)}})
		}
	}
	exact = append(exact, windowExact(entries, norm)...)

	// Fuzzy: conservative single-entry score; windows are left to exact.
	for _, e := range entries {
		score := fuzzyScore(e.norm, norm)
		if score >= FuzzyThreshold {
			fuzzy = append(fuzzy, match{cand: candidate(e, score), span: []string{anchorOf(e)}})
		}
	}
	return exact, fuzzy
}

// windowExact finds consecutive-block windows whose joined normalized text
// equals the chunk exactly (multi-paragraph anchoring, ADR-0048 §4).
func windowExact(entries []entry, norm string) []match {
	// Group by source, preserving order.
	type group struct {
		key  string
		list []entry
	}
	var groups []group
	idx := map[string]int{}
	for _, e := range entries {
		k := e.documentID + "\x00" + e.path
		if i, ok := idx[k]; ok {
			groups[i].list = append(groups[i].list, e)
			continue
		}
		idx[k] = len(groups)
		groups = append(groups, group{key: k, list: []entry{e}})
	}
	var out []match
	for _, g := range groups {
		list := g.list
		sort.SliceStable(list, func(i, j int) bool { return list[i].index < list[j].index })
		for size := 2; size <= maxSpanBlocks && size <= len(list); size++ {
			for start := 0; start+size <= len(list); start++ {
				parts := make([]string, 0, size)
				span := make([]string, 0, size)
				for _, e := range list[start : start+size] {
					parts = append(parts, e.norm)
					span = append(span, anchorOf(e))
				}
				if strings.Join(parts, " ") != norm {
					continue
				}
				first := list[start]
				out = append(out, match{cand: candidate(first, 1.0), span: span})
			}
		}
	}
	return out
}

// candidate builds a wire candidate from an entry.
func candidate(e entry, score float64) dto.LocateCandidate {
	return dto.LocateCandidate{
		DocumentID:  e.documentID,
		Path:        e.path,
		BlockID:     e.blockID,
		ChunkKey:    e.key(),
		Score:       score,
		TextPreview: preview(e.text),
	}
}

// anchorOf is the span/identity element for an entry: the block id when known,
// else the chunk key (corpus files carry no block id).
func anchorOf(e entry) string {
	if e.blockID != "" {
		return e.blockID
	}
	return e.chunkKey
}

// blockEntries converts an open document's blocks into ordered entries.
func blockEntries(blocks []dto.Block, documentID, path string) []entry {
	out := make([]entry, 0, len(blocks))
	for i, b := range blocks {
		n := Normalize(b.Text)
		if n == "" {
			continue
		}
		out = append(out, entry{
			documentID: documentID,
			path:       path,
			blockID:    b.ID,
			chunkKey:   b.ID,
			text:       b.Text,
			norm:       n,
			hash:       NormalizedHash(n),
			index:      i,
		})
	}
	return out
}

// chunkEntries converts indexed chunks into entries, resolving each chunk's
// document id from the status map (empty for path-keyed corpus files).
func chunkEntries(chunks []dto.Chunk, status map[string]dto.IndexedDocument) []entry {
	out := make([]entry, 0, len(chunks))
	for i, c := range chunks {
		n := Normalize(c.Text)
		if n == "" {
			continue
		}
		docID := ""
		if d, ok := status[canonical(c.Path)]; ok {
			docID = d.DocumentID
		}
		out = append(out, entry{
			documentID: docID,
			path:       c.Path,
			blockID:    c.BlockID,
			chunkKey:   c.ChunkKey,
			text:       c.Text,
			norm:       n,
			hash:       NormalizedHash(n),
			index:      chunkIndex(c.ChunkKey, i),
		})
	}
	return out
}

// chunkIndex parses the numeric suffix of a chunk key (path#index); it falls
// back to the slice position so ordering stays deterministic.
func chunkIndex(chunkKey string, fallback int) int {
	if i := strings.LastIndex(chunkKey, "#"); i >= 0 && i+1 < len(chunkKey) {
		if n, err := strconv.Atoi(chunkKey[i+1:]); err == nil {
			return n
		}
	}
	return fallback
}

// dedupe removes duplicate candidates by identity, keeping the first (the
// highest-scored, since callers sort/append in rank order).
func dedupe(in []match) []match {
	seen := map[string]bool{}
	out := in[:0]
	for _, m := range in {
		k := m.cand.ChunkKey
		if k == "" {
			k = m.cand.Path
		}
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, m)
	}
	return out
}

// filterThreshold keeps matches at or above threshold.
func filterThreshold(in []match, threshold float64) []match {
	out := in[:0]
	for _, m := range in {
		if m.cand.Score >= threshold {
			out = append(out, m)
		}
	}
	return out
}

// finish builds a resolved result for one exact match, enriching it with the
// anchor's guard hash and neighbor context when it is the open document.
func (r *resolver) finish(ctx context.Context, m match, req Request, blocks []dto.Block, openPath string, status map[string]dto.IndexedDocument) dto.LocateResult {
	c := m.cand
	res := dto.LocateResult{
		Status:     dto.LocateStatusResolved,
		MatchType:  dto.LocateMatchExact,
		Confidence: c.Score,
		DocumentID: c.DocumentID,
		Path:       c.Path,
		BlockID:    c.BlockID,
		ChunkKey:   c.ChunkKey,
		Span:       m.span,
		Stale:      r.staleFor(ctx, c.Path, status),
	}
	if c.DocumentID == req.DocumentID && c.BlockID != "" {
		res.BaseHash, res.Context = anchorInfo(blocks, c.BlockID, m.span)
	}
	return res
}

// ambiguous builds the ranked picker result.
func (r *resolver) ambiguous(ctx context.Context, matches []match, status map[string]dto.IndexedDocument) dto.LocateResult {
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].cand.Score != matches[j].cand.Score {
			return matches[i].cand.Score > matches[j].cand.Score
		}
		if matches[i].cand.Path != matches[j].cand.Path {
			return matches[i].cand.Path < matches[j].cand.Path
		}
		return matches[i].cand.ChunkKey < matches[j].cand.ChunkKey
	})
	if len(matches) > MaxCandidates {
		matches = matches[:MaxCandidates]
	}
	res := dto.LocateResult{Status: dto.LocateStatusAmbiguous}
	if len(matches) > 0 {
		res.MatchType = dto.LocateMatchFuzzy
		if matches[0].cand.Score >= 1.0 {
			res.MatchType = dto.LocateMatchExact
		}
		res.Confidence = matches[0].cand.Score
	}
	for _, m := range matches {
		c := m.cand
		c.Stale = r.staleFor(ctx, c.Path, status)
		res.Candidates = append(res.Candidates, c)
	}
	return res
}

// anchorInfo returns the anchor block's guard hash and its neighbor window text
// (one block before and after, ADR-0048 §5).
func anchorInfo(blocks []dto.Block, blockID string, span []string) (baseHash, context string) {
	first, last := -1, -1
	for i, b := range blocks {
		if b.ID == blockID {
			first = i
		}
		if len(span) > 0 && b.ID == span[len(span)-1] {
			last = i
		}
	}
	if first < 0 {
		return "", ""
	}
	if last < first {
		last = first
	}
	start := first - NeighborWindow
	if start < 0 {
		start = 0
	}
	end := last + NeighborWindow
	if end >= len(blocks) {
		end = len(blocks) - 1
	}
	parts := make([]string, 0, end-start+1)
	for _, b := range blocks[start : end+1] {
		if b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return blocks[first].Hash, strings.Join(parts, "\n\n")
}

// statusMap indexes Retriever.Status by canonical path for staleness/document
// resolution. A status failure degrades to an empty map (best-effort).
func (r *resolver) statusMap() map[string]dto.IndexedDocument {
	out := map[string]dto.IndexedDocument{}
	rows, err := r.search.Status()
	if err != nil {
		return out
	}
	for _, d := range rows {
		out[canonical(d.Path)] = d
	}
	return out
}

// staleFor reports whether a matched indexed file differs from disk. Only
// path-keyed corpus files are comparable by raw content hash (versioned
// documents index a hash of their chunk texts), so versioned matches are never
// marked stale here; the eventual edit is protected by the base-hash guard.
func (r *resolver) staleFor(ctx context.Context, path string, status map[string]dto.IndexedDocument) bool {
	d, ok := status[canonical(path)]
	if !ok || d.DocumentID != "" {
		return false
	}
	raw, err := r.files.Read(ctx, path, maxFileRead)
	if err != nil {
		return true
	}
	return pathutil.Hash(string(raw)) != d.ContentHash
}

// canonical canonicalizes a path for identity comparison (best-effort).
func canonical(path string) string {
	c, _ := pathutil.Canonical(path)
	return c
}

// preview renders a single-line, length-bounded candidate preview.
func preview(text string) string {
	p := strings.Join(strings.Fields(text), " ")
	if len(p) > previewLen {
		p = p[:previewLen] + "…"
	}
	return p
}
