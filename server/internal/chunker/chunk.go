package chunker

import (
	"errors"
	"strings"

	"texteditor/shared/dto"
)

type chunker struct{}

// New returns the default Chunker implementation.
func New() Chunker { return chunker{} }

var ErrZeroTokens = errors.New("maxTokens must be positive")

// Chunk walks the block tree in document order (parents before children),
// accumulating consecutive leaf blocks into a chunk until adding the next block
// would exceed maxTokens. Non-leaf blocks (those with a child) are treated as
// transparent containers: their text is added to the current chunk but does not
// force a boundary by itself.
//
// The token estimator counts a word as one token; this is a documented
// approximation sufficient for paragraph-aligned sizing, not authoritative
// metering (the assembler/meter own authoritative counts).
func (chunker) Chunk(tree []dto.Block, maxTokens int) ([]dto.Chunk, error) {
	if maxTokens <= 0 {
		return nil, ErrZeroTokens
	}

	children := map[string][]dto.Block{}
	byID := map[string]dto.Block{}
	var roots []dto.Block
	for _, b := range tree {
		byID[b.ID] = b
		if b.ParentID == nil {
			roots = append(roots, b)
		} else {
			children[*b.ParentID] = append(children[*b.ParentID], b)
		}
	}

	var chunks []dto.Chunk
	var curTexts []string
	curTokens := 0
	curBlockID := ""

	flush := func() {
		if curTokens == 0 {
			return
		}
		text := strings.Join(curTexts, "\n\n")
		chunks = append(chunks, dto.Chunk{
			BlockID: curBlockID,
			Text:    text,
			Source:  curBlockID,
		})
		curTexts = nil
		curTokens = 0
		curBlockID = ""
	}

	add := func(b dto.Block) {
		toks := estimate(b.Text)
		if b.Text == "" {
			return
		}
		// A single block larger than maxTokens becomes its own (oversized) chunk.
		if curTokens > 0 && curTokens+toks > maxTokens {
			flush()
		}
		if curTokens == 0 {
			curBlockID = b.ID // chunk anchors at its first block
		}
		curTexts = append(curTexts, b.Text)
		curTokens += toks
	}

	var walk func(list []dto.Block)
	walk = func(list []dto.Block) {
		for _, b := range list {
			add(b)
			walk(children[b.ID])
		}
	}
	walk(roots)
	flush()

	return chunks, nil
}

// estimate words+ as a token proxy (documented approximation).
func estimate(text string) int {
	f := strings.Fields(text)
	return len(f)
}
