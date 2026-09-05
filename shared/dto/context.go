package dto

// Message is one conversation entry (role ∈ user | assistant | tool)
// (interface.md §0b).
type Message struct {
	Role      string // user | assistant | tool
	Content   string // tool messages carry the tool result (JSON) in Content
	Timestamp int64  // unix epoch seconds
}

// BlockKind enumerates Markdown block element kinds (interface.md §4b).
type BlockKind string

const (
	BlockKindParagraph  BlockKind = "paragraph"
	BlockKindHeading    BlockKind = "heading"
	BlockKindListItem   BlockKind = "list_item"
	BlockKindCodeFence  BlockKind = "code_fence"
	BlockKindBlockquote BlockKind = "blockquote"
	BlockKindTable      BlockKind = "table"
)

// Block is a Markdown block element in the document tree (interface.md §0b,
// ADR-0020 §3).
type Block struct {
	ID       string    // stable UUID, minted at creation (ADR-0020 §3)
	ParentID *string   // nil = root level
	Kind     BlockKind // paragraph | heading | list_item | code_fence | blockquote | table
	Position int       // sibling order
	Text     string    // canonical content (normalized + formatted, ADR-0029)
	Hash     string    // hash of the canonical Text — the guard anchor (ADR-0029)
}

// Document is document metadata; the content is the block tree, read via
// DocumentStore.Blocks (interface.md §0b).
type Document struct {
	ID          string // surrogate id (UUID)
	Path        string // absolute path; the Document store's open resolver
	RootBlockID string // id of the root block
	UpdatedAt   int64  // unix epoch seconds
}

// Chunk is a retrieved passage (interface.md §3).
type Chunk struct {
	BlockID string
	Text    string
	Score   float32
	Source  string // citation/provenance marker
}
