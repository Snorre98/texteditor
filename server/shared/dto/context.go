package dto

import "encoding/json"

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
// ChunkKey/Path/Heading are the ADR-0044 §3 / ADR-0049 §4 provenance additions:
// chunkKey is path#index for corpus files and documentID#index for versioned
// documents; path is the canonical file path; heading is the nearest heading
// (markdown heading stack for corpus files, preceding heading for block trees).
type Chunk struct {
	BlockID  string  `json:"blockId"`
	ChunkKey string  `json:"chunkKey,omitempty"`
	Text     string  `json:"text"`
	Score    float32 `json:"score"`
	Source   string  `json:"source"` // citation/provenance marker
	Path     string  `json:"path,omitempty"`
	Heading  string  `json:"heading,omitempty"`
}

// IndexedDocument is one indexed file's status row (Retriever.Status,
// interface.md §3): the engine's last-known content hash and chunk count, so the
// corpus layer can detect stale disk files (ADR-0049 §4).
type IndexedDocument struct {
	Path        string
	DocumentID  string // empty for path-keyed corpus files
	ContentHash string
	ChunkCount  int
	IndexedAt   int64
}

// ChunkRef is a stable reference to one indexed chunk for tray pin/exclude
// decisions (ADR-0049 §8, api/openapi.yaml ChunkRef).
type ChunkRef struct {
	Path     string `json:"path"`
	ChunkKey string `json:"chunkKey"`
	Hash     string `json:"hash,omitempty"`
}

// ContextMessage is one assembled message's component and provenance in a
// context snapshot (interface.md §5, ADR-0044 §4). It is the wire form of
// MessageProvenance; the snapshot is engine-owned data clients render verbatim.
type ContextMessage struct {
	Role      string `json:"role"`      // system | user | assistant | tool
	Component string `json:"component"` // system | history | rag | mention | user
	Source    string `json:"source,omitempty"`
	Tokens    int    `json:"tokens"`
	Pinned    bool   `json:"pinned"` // human override (Phase C4; false in C3)
}

// ContextDrop is one labeled truncation/drop record (interface.md §5,
// ADR-0044 §3). Truncation is never silent.
type ContextDrop struct {
	Component string `json:"component"` // history | rag | mention
	Reason    string `json:"reason"`
	Count     int    `json:"count"`
	Detail    string `json:"detail,omitempty"`
}

// BudgetUsage is one component's budget utilization (interface.md §5,
// ADR-0044 §4): the deterministic estimate used vs the PipelinePolicy limit.
type BudgetUsage struct {
	Component string `json:"component"` // system|tools|rag|history|mentions|user|thinking
	Used      int    `json:"used"`
	Limit     int    `json:"limit,omitempty"` // 0/absent when no policy limit applies
}

// ContextSnapshot is the engine-owned, persisted record of one turn's assembled
// context (interface.md §5/§7, ADR-0044 §4, ADR-0049 §7). The snapshot itself
// is the contract: clients render it and never reconstruct provenance, budgets,
// or drops. Decision (Phase F) and Locate (Phase D) are reserved optional
// records, unimplemented in C3.
type ContextSnapshot struct {
	TurnID         string           `json:"turnId"`
	SessionID      string           `json:"sessionId"`
	WorkspaceID    string           `json:"workspaceId"`
	RetrievalQuery string           `json:"retrievalQuery"`
	AutoRag        bool             `json:"autoRag"`
	Messages       []ContextMessage `json:"messages"`
	Chunks         []Chunk          `json:"chunks"`
	Drops          []ContextDrop    `json:"drops"`
	Budget         []BudgetUsage    `json:"budget"`
	Decision       json.RawMessage  `json:"decision,omitempty"`
	Locate         json.RawMessage  `json:"locate,omitempty"`
	CreatedAt      int64            `json:"createdAt"`
}
