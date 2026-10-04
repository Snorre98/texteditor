package dto

// Workspace is a persistent engine entity: a root directory plus its
// subdirectories, governing browsing and editing only (ADR-0049 §1/§2). It is
// the unit that owns a session list, a corpus, and a storage shard.
type Workspace struct {
	ID        string // UUID, client-facing identity
	Root      string // canonical absolute directory (EvalSymlinks + case-fold)
	Name      string // display label (default: basename of Root)
	CreatedAt int64
	UpdatedAt int64
}

// DefaultCorpusInclude is the corpus's default include glob (ADR-0049 §3).
const DefaultCorpusInclude = "**/*.md"

// CorpusScope is a workspace's corpus: a set of roots (each a file or a
// directory) plus include/exclude globs (ADR-0049 §3). It governs
// retrievability only; the workspace root never bounds it.
type CorpusScope struct {
	Roots   []string // canonical absolute paths
	Include []string // globs; default **/*.md
	Exclude []string // globs
}

// CorpusDocumentStatus is one corpus file's index status (ADR-0049 §4).
type CorpusDocumentStatus struct {
	ID         string // stable path-derived id (pathutil.DocID)
	Path       string // canonical absolute path
	Status     string // indexed | stale | pending | evicted | error
	ChunkCount int
	IndexedAt  int64
	Error      string // populated when Status == "error"
}

// CorpusJob is one observable indexing job (ADR-0049 §16; polling via
// GET /corpus per the Phase C pin).
type CorpusJob struct {
	ID          string
	WorkspaceID string
	Kind        string // reconcile | index | path
	State       string // running | done | error
	Total       int
	Completed   int
	Error       string
	StartedAt   int64
	FinishedAt  int64
}

// CorpusState is a workspace's corpus scope plus per-document status and the
// latest job (the GET /corpus payload).
type CorpusState struct {
	WorkspaceID string
	Roots       []string
	Include     []string
	Exclude     []string
	Documents   []CorpusDocumentStatus
	Job         *CorpusJob
}
