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
