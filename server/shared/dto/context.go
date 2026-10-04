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
	// Pinned/HumanOverride label a chunk the author explicitly pinned (ADR-0049
	// §11): it bypasses the decision gate but never budgets. Set only on
	// snapshot chunks; auto-retrieved `rag` event chunks stay unlabeled.
	Pinned        bool `json:"pinned,omitempty"`
	HumanOverride bool `json:"humanOverride,omitempty"`
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

// DecisionOverride is the session/per-turn override for the Laya decision layer
// (ADR-0055 §2): the same three values as the global DecisionMode. off runs no
// Laya call (zero calls, a strict no-op); planner runs only the planner;
// planner+gate runs the planner and the per-chunk gate.
type DecisionOverride string

const (
	DecisionOff         DecisionOverride = "off"
	DecisionPlanner     DecisionOverride = "planner"
	DecisionPlannerGate DecisionOverride = "planner+gate"
)

// ContextPolicy is the context-tray decision set (ADR-0049 §7/§8): pinned and
// excluded chunk refs, an auto-RAG flag, and a retrieval query. The engine
// persists one per session (Session.contextPolicy) and accepts per-turn
// overrides (Task.Context). Merge is replace-when-present per field: a non-nil
// slice / non-nil pointer replaces the lower layer wholesale (an explicit empty
// list clears), and a nil field inherits. It carries no payload text — the
// client sends decisions, the engine assembles.
type ContextPolicy struct {
	Pinned         []ChunkRef        `json:"pinned,omitempty"`
	Excluded       []ChunkRef        `json:"excluded,omitempty"`
	AutoRag        *bool             `json:"autoRag,omitempty"`
	RetrievalQuery *string           `json:"retrievalQuery,omitempty"`
	Thinking       *ThinkingLevel    `json:"thinking,omitempty"`
	Decision       *DecisionOverride `json:"decision,omitempty"`
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
// ADR-0044 §3). Truncation is never silent. HumanOverride marks a dropped
// human override (a pinned chunk truncated for budget, ADR-0049 §11).
type ContextDrop struct {
	Component     string `json:"component"` // history | rag | mention
	Reason        string `json:"reason"`
	Count         int    `json:"count"`
	Detail        string `json:"detail,omitempty"`
	HumanOverride bool   `json:"humanOverride,omitempty"`
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
// records. Thinking/Measurements/Compacted/Window are the ADR-0051 additions.
type ContextSnapshot struct {
	TurnID         string            `json:"turnId"`
	SessionID      string            `json:"sessionId"`
	WorkspaceID    string            `json:"workspaceId"`
	RetrievalQuery string            `json:"retrievalQuery"`
	AutoRag        bool              `json:"autoRag"`
	Messages       []ContextMessage  `json:"messages"`
	Chunks         []Chunk           `json:"chunks"`
	Drops          []ContextDrop     `json:"drops"`
	Budget         []BudgetUsage     `json:"budget"`
	Decision       *DecisionRecord   `json:"decision,omitempty"`
	Locate         json.RawMessage   `json:"locate,omitempty"`
	Thinking       *ThinkingSnapshot `json:"thinking,omitempty"`
	Measurements   *TurnMeasurement  `json:"measurements,omitempty"`
	Compacted      *CompactionRecord `json:"compacted,omitempty"`
	Window         *WindowUsage      `json:"window,omitempty"`
	SessionBudget  *SessionBudget    `json:"sessionBudget,omitempty"`
	// Cancelled labels a user-cancelled turn (ADR-0046 E2); partial usage is
	// still metered. Absent/false for a normal turn.
	Cancelled bool  `json:"cancelled,omitempty"`
	CreatedAt int64 `json:"createdAt"`
}

// SessionBudget is the session budget state for one turn (ADR-0051 §7): the
// soft warning is a label (the turn proceeds); the hard threshold refuses.
type SessionBudget struct {
	Soft   bool `json:"soft,omitempty"`
	Hard   bool `json:"hard,omitempty"`
	Used   int  `json:"used"`
	Budget int  `json:"budget"`
}

// ThinkingSnapshot is the turn's resolved thinking outcome (ADR-0051 §1–§5):
// the policy level, the level actually used, and the labeled
// degradation/escalation/truncation outcome.
type ThinkingSnapshot struct {
	Level            ThinkingLevel `json:"level"`
	Effective        bool          `json:"effective"`
	Escalated        bool          `json:"escalated,omitempty"`
	EscalationReason string        `json:"escalationReason,omitempty"`
	Unsupported      bool          `json:"unsupported,omitempty"`
	Truncated        bool          `json:"truncated,omitempty"`
}

// TurnMeasurement is the per-turn measurement record (ADR-0051 §11): token
// counts, wall-clock latency, model + quant, and window utilization, recorded
// per model so the hardware map accumulates.
type TurnMeasurement struct {
	PromptTokens      int     `json:"promptTokens"`
	ThinkingTokens    int     `json:"thinkingTokens"`
	CompletionTokens  int     `json:"completionTokens"`
	LatencyMs         int64   `json:"latencyMs"`
	Model             string  `json:"model,omitempty"`
	Quant             string  `json:"quant,omitempty"`
	WindowUtilization float64 `json:"windowUtilization,omitempty"`
}

// CompactionRecord is the summarized history range replaced by one metered
// summary message (ADR-0051 §8).
type CompactionRecord struct {
	FromTs        int64 `json:"fromTs"`
	ToTs          int64 `json:"toTs"`
	Turns         int   `json:"turns"`
	SummaryTokens int   `json:"summaryTokens"`
	CacheCost     bool  `json:"cacheCost,omitempty"`
}

// WindowUsage is the per-turn context-window accounting (ADR-0051 §6): the
// assembled payload plus output reserve against the model's context window.
type WindowUsage struct {
	ContextLength int     `json:"contextLength"`
	Used          int     `json:"used"`
	Reserve       int     `json:"reserve"`
	Utilization   float64 `json:"utilization"`
}
