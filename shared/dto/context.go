package dto

// Message is one conversation entry (role ∈ user | assistant | tool)
// (interface.md §0b).
type Message struct {
	Role      string // user | assistant | tool
	Content   string // tool messages carry the tool result (JSON) in Content
	Timestamp int64  // unix epoch seconds
}

// ToolCall is one native tool-calling invocation emitted by the model
// (interface.md §2, amended at the agentic-loop milestone). It is the assembled
// form of the wire delta.tool_calls: id (for the role:"tool" response), name (a
// registered ToolDef.Name), and the JSON arguments string. Owner-free DTO.
type ToolCall struct {
	ID        string // the assistant's tool_call id, echoed back in the tool message
	Name      string // the real tool name (== ToolDef.Name)
	Arguments string // JSON-encoded arguments (rendered verbatim to Invoke)
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

// Chunk is a retrieved passage (interface.md §3). JSON tags are camelCase: the
// chunk crosses the API wire via the `rag` SSE event (recorded amendment).
type Chunk struct {
	BlockID string  `json:"blockId"`
	Text    string  `json:"text"`
	Score   float32 `json:"score"`
	Source  string  `json:"source"` // citation/provenance marker
}
