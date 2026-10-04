package chunker

import (
	"strings"
	"testing"

	"texteditor/shared/dto"
)

func TestChunkParagraphAligned(t *testing.T) {
	c := New()

	p1 := strings.Repeat("alpha ", 20)
	p2 := strings.Repeat("bravo ", 20)
	p3 := strings.Repeat("charlie ", 20)

	tree := []dto.Block{
		{ID: "r1", ParentID: nil, Kind: dto.BlockKindHeading, Text: "# Title"},
		{ID: "p1", ParentID: nil, Kind: dto.BlockKindParagraph, Text: p1},
		{ID: "p2", ParentID: nil, Kind: dto.BlockKindParagraph, Text: p2},
		{ID: "p3", ParentID: nil, Kind: dto.BlockKindParagraph, Text: p3},
	}

	// 25 tokens per chunk forces boundaries between paragraphs.
	chunks, err := c.Chunk(tree, 25)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 3 {
		t.Fatalf("got %d chunks, want 3", len(chunks))
	}
	// Each block's text must appear whole in exactly one chunk (no splitting a
	// paragraph across chunks).
	for i, b := range tree {
		texts := []string{"# Title", p1, p2, p3}
		count := 0
		for _, ch := range chunks {
			if strings.Contains(ch.Text, texts[i]) {
				count++
			}
		}
		if count != 1 {
			t.Errorf("block %s text appears in %d chunks, want exactly 1", b.ID, count)
		}
	}
	// Chunk order must follow document order.
	if chunks[0].BlockID != "r1" {
		t.Errorf("first chunk anchor = %q, want r1", chunks[0].BlockID)
	}
}

func TestChunkSingleOversizedBlock(t *testing.T) {
	c := New()
	tree := []dto.Block{
		{ID: "p1", ParentID: nil, Kind: dto.BlockKindParagraph, Text: strings.Repeat("word ", 100)},
	}
	chunks, err := c.Chunk(tree, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 1 {
		t.Fatalf("got %d chunks, want 1", len(chunks))
	}
}

func TestChunkNestedTreeOrder(t *testing.T) {
	c := New()
	chunks, err := c.Chunk([]dto.Block{
		{ID: "h", ParentID: nil, Kind: dto.BlockKindHeading, Text: "# H"},
		{ID: "li1", ParentID: strPtr("h"), Kind: dto.BlockKindListItem, Text: "one"},
		{ID: "li2", ParentID: strPtr("h"), Kind: dto.BlockKindListItem, Text: "two"},
	}, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 1 {
		t.Fatalf("got %d chunks, want 1 (all nested under one) ", len(chunks))
	}
	if !strings.Contains(chunks[0].Text, "# H") || !strings.Contains(chunks[0].Text, "one") || !strings.Contains(chunks[0].Text, "two") {
		t.Errorf("nested order wrong: %q", chunks[0].Text)
	}
}

func TestChunkRejectsZeroTokens(t *testing.T) {
	c := New()
	if _, err := c.Chunk(nil, 0); err != ErrZeroTokens {
		t.Fatalf("got %v, want ErrZeroTokens", err)
	}
}

func strPtr(s string) *string { return &s }

// TestChunkHeadingLabels covers the heading provenance added by ADR-0044 §3:
// chunks under a heading carry the heading text, and a new heading flushes.
func TestChunkHeadingLabels(t *testing.T) {
	c := New()
	chunks, err := c.Chunk([]dto.Block{
		{ID: "h1", ParentID: nil, Kind: dto.BlockKindHeading, Text: "# Intro"},
		{ID: "p1", ParentID: nil, Kind: dto.BlockKindParagraph, Text: "alpha beta"},
		{ID: "h2", ParentID: nil, Kind: dto.BlockKindHeading, Text: "# Methods"},
		{ID: "p2", ParentID: nil, Kind: dto.BlockKindParagraph, Text: "gamma delta"},
	}, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 2 {
		t.Fatalf("got %d chunks, want 2 (heading boundary): %+v", len(chunks), chunks)
	}
	if chunks[0].Heading != "# Intro" || chunks[1].Heading != "# Methods" {
		t.Fatalf("headings = %q, %q", chunks[0].Heading, chunks[1].Heading)
	}
}

func TestChunkMarkdownHeadingBoundaries(t *testing.T) {
	raw := "# Intro\n\nalpha beta gamma.\n\n## Details\n\ndelta epsilon zeta.\n"
	chunks, err := ChunkMarkdown(raw, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 2 {
		t.Fatalf("got %d chunks, want 2: %+v", len(chunks), chunks)
	}
	if chunks[0].Heading != "Intro" {
		t.Fatalf("chunk0 heading = %q, want Intro", chunks[0].Heading)
	}
	if chunks[1].Heading != "Intro > Details" {
		t.Fatalf("chunk1 heading = %q, want Intro > Details", chunks[1].Heading)
	}
	if !strings.Contains(chunks[0].Text, "# Intro") || !strings.Contains(chunks[0].Text, "alpha beta gamma.") {
		t.Fatalf("chunk0 text = %q", chunks[0].Text)
	}
	if !strings.Contains(chunks[1].Text, "## Details") || !strings.Contains(chunks[1].Text, "delta epsilon zeta.") {
		t.Fatalf("chunk1 text = %q", chunks[1].Text)
	}
}

func TestChunkMarkdownOversizedParagraphOwnChunk(t *testing.T) {
	raw := "# A\n\n" + strings.Repeat("word ", 100) + "\n\nshort tail\n"
	chunks, err := ChunkMarkdown(raw, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) < 2 {
		t.Fatalf("got %d chunks, want >= 2", len(chunks))
	}
	// The oversized paragraph is never split across chunks.
	big := strings.TrimSpace(strings.Repeat("word ", 100))
	count := 0
	for _, ch := range chunks {
		if strings.Contains(ch.Text, big) {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("oversized paragraph appears in %d chunks, want 1", count)
	}
}

func TestChunkMarkdownFenceHeadingsIgnored(t *testing.T) {
	raw := "# Real\n\n```\n# not a heading\n```\n\ntail text\n"
	chunks, err := ChunkMarkdown(raw, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 1 {
		t.Fatalf("got %d chunks, want 1 (fence heading ignored): %+v", len(chunks), chunks)
	}
	if chunks[0].Heading != "Real" {
		t.Fatalf("heading = %q, want Real", chunks[0].Heading)
	}
	if !strings.Contains(chunks[0].Text, "# not a heading") {
		t.Fatalf("fence content lost: %q", chunks[0].Text)
	}
}

func TestChunkMarkdownRejectsZeroTokens(t *testing.T) {
	if _, err := ChunkMarkdown("x", 0); err != ErrZeroTokens {
		t.Fatalf("got %v, want ErrZeroTokens", err)
	}
}
