package locate

import (
	"context"
	"errors"
	"testing"

	"texteditor/internal/pathutil"
	"texteditor/shared/dto"
)

// --------------------------- fakes ---------------------------

type stubDocs struct {
	blocks []dto.Block
	path   string
	err    error
}

func (s *stubDocs) Blocks(string) ([]dto.Block, error) { return s.blocks, s.err }
func (s *stubDocs) Path(string) (string, error)        { return s.path, s.err }

type stubSearcher struct {
	chunks []dto.Chunk
	status []dto.IndexedDocument
	err    error
}

func (s *stubSearcher) SearchText(context.Context, string, int) ([]dto.Chunk, error) {
	return s.chunks, s.err
}
func (s *stubSearcher) Status() ([]dto.IndexedDocument, error) { return s.status, s.err }

type stubFiles struct {
	content map[string]string
	err     map[string]error
}

func (s *stubFiles) Read(_ context.Context, path string, _ int) ([]byte, error) {
	if e, ok := s.err[path]; ok {
		return nil, e
	}
	return []byte(s.content[path]), nil
}

func block(id, text string) dto.Block {
	return dto.Block{ID: id, Text: text, Hash: "hash-" + id}
}

func resolverWith(docs *stubDocs, search *stubSearcher, files *stubFiles) Resolver {
	if files == nil {
		files = &stubFiles{content: map[string]string{}, err: map[string]error{}}
	}
	return New(docs, search, files)
}

// --------------------------- normalize ---------------------------

func TestNormalizeMarkdownAndWhitespace(t *testing.T) {
	cases := []struct{ a, b string }{
		{"## Heading\n\nSome *emphasized* text [link](http://x).", "Heading\n\nSome   emphasized text link."},
		{"- item one\n- item two", "item one item two"},
		{"1. first\n2. second", "first second"},
		{"> quoted line", "quoted line"},
		{"plain   words\nover lines", "plain words over lines"},
	}
	for _, c := range cases {
		if got, want := Normalize(c.a), Normalize(c.b); got != want {
			t.Fatalf("Normalize(%q)=%q, Normalize(%q)=%q", c.a, got, c.b, want)
		}
	}
}

func TestNormalizedHashStable(t *testing.T) {
	if NormalizedHash(Normalize("Hello   World")) != NormalizedHash(Normalize("hello world")) {
		t.Fatal("normalized hash should ignore case and whitespace")
	}
}

// --------------------------- feature scenarios ---------------------------

// Scenario: An exact paste resolves to its block.
func TestExactPasteResolves(t *testing.T) {
	docs := &stubDocs{
		blocks: []dto.Block{
			block("b1", "An unrelated opening paragraph."),
			block("b2", "The quick brown fox jumps over the lazy dog."),
		},
		path: "/vault/doc.md",
	}
	r := resolverWith(docs, &stubSearcher{}, nil)
	res, err := r.Resolve(context.Background(), Request{
		Chunk:      "The quick brown fox jumps over the lazy dog.",
		DocumentID: "d1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != dto.LocateStatusResolved || res.MatchType != dto.LocateMatchExact {
		t.Fatalf("result = %+v, want resolved/exact", res)
	}
	if res.BlockID != "b2" || res.DocumentID != "d1" || res.Path != "/vault/doc.md" {
		t.Fatalf("anchor = %+v, want b2/d1//vault/doc.md", res)
	}
	if res.Confidence != 1.0 {
		t.Fatalf("confidence = %v, want 1.0", res.Confidence)
	}
	if res.BaseHash != "hash-b2" {
		t.Fatalf("base hash = %q, want hash-b2", res.BaseHash)
	}
	if res.Context == "" {
		t.Fatal("context should carry the anchored block + neighbors")
	}
}

// Scenario: Whitespace and markdown differences still resolve.
func TestWhitespaceAndMarkdownDifferencesResolve(t *testing.T) {
	docs := &stubDocs{
		blocks: []dto.Block{block("b1", "Some *emphasized* text with a [link](http://x).")},
		path:   "/vault/doc.md",
	}
	r := resolverWith(docs, &stubSearcher{}, nil)
	res, err := r.Resolve(context.Background(), Request{
		Chunk:      "Some   emphasized text with a link.",
		DocumentID: "d1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != dto.LocateStatusResolved || res.BlockID != "b1" {
		t.Fatalf("result = %+v, want resolved b1", res)
	}
}

// Scenario: Fuzzy matches require confirmation.
func TestFuzzyRequiresConfirmation(t *testing.T) {
	docs := &stubDocs{
		blocks: []dto.Block{block("b1", "The quick brown fox jumps over the lazy dog.")},
		path:   "/vault/doc.md",
	}
	r := resolverWith(docs, &stubSearcher{}, nil)
	res, err := r.Resolve(context.Background(), Request{
		Chunk:      "The quick brown fox leaps over the lazy dog.",
		DocumentID: "d1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != dto.LocateStatusAmbiguous || res.MatchType != dto.LocateMatchFuzzy {
		t.Fatalf("result = %+v, want ambiguous/fuzzy", res)
	}
	if len(res.Candidates) != 1 {
		t.Fatalf("candidates = %d, want 1", len(res.Candidates))
	}
	if res.Candidates[0].BlockID != "b1" || res.Candidates[0].Score < FuzzyThreshold {
		t.Fatalf("candidate = %+v, want b1 above threshold", res.Candidates[0])
	}
}

// Duplicate text is ambiguous rather than an arbitrary anchor.
func TestDuplicateExactMatchesAreAmbiguous(t *testing.T) {
	docs := &stubDocs{
		blocks: []dto.Block{
			block("b1", "Repeated sentence."),
			block("b2", "Repeated sentence."),
		},
		path: "/vault/doc.md",
	}
	r := resolverWith(docs, &stubSearcher{}, nil)
	res, err := r.Resolve(context.Background(), Request{Chunk: "Repeated sentence.", DocumentID: "d1"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != dto.LocateStatusAmbiguous {
		t.Fatalf("result = %+v, want ambiguous", res)
	}
	if len(res.Candidates) != 2 {
		t.Fatalf("candidates = %d, want 2", len(res.Candidates))
	}
}

// Multi-paragraph chunks match a window of consecutive blocks; the anchor is the
// first block and the span names them all.
func TestMultiParagraphSpan(t *testing.T) {
	docs := &stubDocs{
		blocks: []dto.Block{
			block("b1", "First paragraph."),
			block("b2", "Second paragraph."),
			block("b3", "Third paragraph."),
		},
		path: "/vault/doc.md",
	}
	r := resolverWith(docs, &stubSearcher{}, nil)
	res, err := r.Resolve(context.Background(), Request{
		Chunk:      "First paragraph.\n\nSecond paragraph.",
		DocumentID: "d1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != dto.LocateStatusResolved {
		t.Fatalf("result = %+v, want resolved", res)
	}
	if res.BlockID != "b1" {
		t.Fatalf("anchor = %q, want b1", res.BlockID)
	}
	if len(res.Span) != 2 || res.Span[0] != "b1" || res.Span[1] != "b2" {
		t.Fatalf("span = %v, want [b1 b2]", res.Span)
	}
}

// Scenario: Not found degrades to plain chat.
func TestNotFound(t *testing.T) {
	docs := &stubDocs{blocks: []dto.Block{block("b1", "Totally unrelated prose.")}, path: "/vault/doc.md"}
	r := resolverWith(docs, &stubSearcher{}, nil)
	res, err := r.Resolve(context.Background(), Request{
		Chunk:      "A completely different discussion of quantum chromodynamics.",
		DocumentID: "d1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != dto.LocateStatusNotFound {
		t.Fatalf("result = %+v, want not-found", res)
	}
}

// The vault index is searched when the open document has no match.
func TestVaultIndexFallback(t *testing.T) {
	docs := &stubDocs{blocks: []dto.Block{block("b1", "open doc text")}, path: "/vault/open.md"}
	search := &stubSearcher{
		chunks: []dto.Chunk{{
			ChunkKey: "/vault/other.md#0",
			Path:     "/vault/other.md",
			Text:     "A passage from another note in the vault.",
		}},
		status: []dto.IndexedDocument{{
			Path:        "/vault/other.md",
			DocumentID:  "d2",
			ContentHash: pathutil.Hash("A passage from another note in the vault."),
		}},
	}
	r := resolverWith(docs, search, nil)
	res, err := r.Resolve(context.Background(), Request{
		Chunk:      "A passage from another note in the vault.",
		DocumentID: "d1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != dto.LocateStatusResolved {
		t.Fatalf("result = %+v, want resolved", res)
	}
	if res.DocumentID != "d2" || res.Path != "/vault/other.md" || res.ChunkKey != "/vault/other.md#0" {
		t.Fatalf("result = %+v, want d2//vault/other.md#0", res)
	}
	// A different document is not anchorable to the open document.
	if res.BaseHash != "" || res.Context != "" {
		t.Fatalf("cross-document match should not carry an anchor: %+v", res)
	}
}

// Stale text: the indexed corpus file differs from disk.
func TestStaleCorpusMatch(t *testing.T) {
	search := &stubSearcher{
		chunks: []dto.Chunk{{
			ChunkKey: "/vault/note.md#0",
			Path:     "/vault/note.md",
			Text:     "Stale indexed passage.",
		}},
		status: []dto.IndexedDocument{{
			Path:        "/vault/note.md",
			ContentHash: "old-hash",
		}},
	}
	files := &stubFiles{content: map[string]string{"/vault/note.md": "changed on disk"}, err: map[string]error{}}
	r := resolverWith(&stubDocs{}, search, files)
	res, err := r.Resolve(context.Background(), Request{Chunk: "Stale indexed passage."})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != dto.LocateStatusResolved || !res.Stale {
		t.Fatalf("result = %+v, want resolved + stale", res)
	}
}

// A search error degrades to not-found (fail-open); locate never blocks a turn.
func TestSearchErrorFailsOpen(t *testing.T) {
	r := resolverWith(&stubDocs{}, &stubSearcher{err: errors.New("boom")}, nil)
	res, err := r.Resolve(context.Background(), Request{Chunk: "anything"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != dto.LocateStatusNotFound {
		t.Fatalf("result = %+v, want not-found", res)
	}
}

// An empty chunk is not-found.
func TestEmptyChunkNotFound(t *testing.T) {
	r := resolverWith(&stubDocs{blocks: []dto.Block{block("b1", "x")}, path: "/v"}, &stubSearcher{}, nil)
	res, _ := r.Resolve(context.Background(), Request{Chunk: "   "})
	if res.Status != dto.LocateStatusNotFound {
		t.Fatalf("result = %+v, want not-found", res)
	}
}
