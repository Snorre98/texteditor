// Package chunker is a pure, deterministic leaf that splits a document block
// tree into paragraph-aligned, size-bounded chunks (ADR-0020 §5).
package chunker

import "texteditor/shared/dto"

// Interface is the Chunker public API (interface.md §4).
type Interface interface {
	// Chunk split a document block tree into []Chunk, paragraph-aligned and
	// size-bounded by maxTokens. Chunk size is a data tunable — the RAG token
	// lever (ADR-0020 §5).
	Chunk(tree []dto.Block, maxTokens int) ([]dto.Chunk, error)
}
