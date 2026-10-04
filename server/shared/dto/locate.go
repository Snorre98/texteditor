package dto

// Locate is the `/locate` command's deterministic chunk-anchoring result
// (ADR-0048). It is engine-owned data: recorded in the turn's ContextSnapshot
// (`locate`), emitted as the `locate` SSE event, and returned by
// POST /turns/{id}/locate for the ambiguity picker. The resolver never calls a
// model or the embedding path (ADR-0048 §2).
type LocateResult struct {
	// TurnID is populated only on the emitted `locate` SSE event so a client can
	// answer the ambiguity picker via POST /turns/{id}/locate; the per-turn SSE
	// framing carries no turn id otherwise. It is absent from the snapshot record
	// (the snapshot envelope already has turnId).
	TurnID string `json:"turnId,omitempty"`
	// Status is resolved | ambiguous | not-found (ADR-0048 §3/§4).
	Status string `json:"status"`
	// MatchType is exact | fuzzy when resolved or ambiguous.
	MatchType string `json:"matchType,omitempty"`
	// Confidence is 1.0 for an exact normalized match, else the fuzzy score.
	Confidence float64 `json:"confidence,omitempty"`
	// DocumentID is the matched versioned document, when known.
	DocumentID string `json:"documentId,omitempty"`
	// Path is the matched file's canonical absolute path.
	Path string `json:"path,omitempty"`
	// BlockID is the anchor block (the FIRST block of a multi-block span).
	BlockID string `json:"blockId,omitempty"`
	// ChunkKey is the matched chunk's stable key when the match came from the
	// corpus index (path#index | documentID#index).
	ChunkKey string `json:"chunkKey,omitempty"`
	// Span is the first..last block ids of a multi-block match.
	Span []string `json:"span,omitempty"`
	// Candidates is the ranked list for an ambiguous outcome (fuzzy always
	// requires confirmation; multiple exact matches are ambiguous too).
	Candidates []LocateCandidate `json:"candidates,omitempty"`
	// Stale marks a match whose indexed file differs from disk.
	Stale bool `json:"stale,omitempty"`
	// Context is the anchored block plus its neighbors (the mention text) or a
	// labeled degrade reason ("locate-cancelled") on a fail-open outcome.
	Context string `json:"context,omitempty"`
	// BaseHash is the anchor block's guard hash (ADR-0029 §4); engine-internal.
	BaseHash string `json:"-"`
}

// LocateCandidate is one ranked anchor candidate in an ambiguous LocateResult
// (ADR-0048 §4). ChunkKey is the picker's choice key.
type LocateCandidate struct {
	DocumentID  string  `json:"documentId,omitempty"`
	Path        string  `json:"path"`
	BlockID     string  `json:"blockId,omitempty"`
	ChunkKey    string  `json:"chunkKey"`
	Score       float64 `json:"score"`
	TextPreview string  `json:"textPreview"`
	Stale       bool    `json:"stale,omitempty"`
}

// LocateChoice is the ambiguity picker's answer (POST /turns/{id}/locate,
// ADR-0048 §4): pick a candidate by its chunkKey, or cancel to plain chat.
type LocateChoice struct {
	ChunkKey string `json:"chunkKey,omitempty"`
	Cancel   bool   `json:"cancel,omitempty"`
}

// Locate status/matchType values (ADR-0048 §3).
const (
	LocateStatusResolved  = "resolved"
	LocateStatusAmbiguous = "ambiguous"
	LocateStatusNotFound  = "not-found"

	LocateMatchExact = "exact"
	LocateMatchFuzzy = "fuzzy"
)
