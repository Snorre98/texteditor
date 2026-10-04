package dto

// ThinkingLevel is the thinking policy level (ADR-0051 §1): off | auto | on.
type ThinkingLevel string

const (
	// ThinkingOff asks the runner to disable its thinking channel.
	ThinkingOff ThinkingLevel = "off"
	// ThinkingAuto runs thinking-off first and escalates once after a structured
	// failure (ADR-0051 §2).
	ThinkingAuto ThinkingLevel = "auto"
	// ThinkingOn always thinks.
	ThinkingOn ThinkingLevel = "on"
)

// CompactionPolicy is the metered history-compaction policy (ADR-0051 §8/§10).
type CompactionPolicy struct {
	Enabled              bool
	TriggerHistoryTokens int
	KeepRecentTurns      int
}

// PipelinePolicy is the one global turn pipeline policy (ADR-0045 §3): the step
// cap, the context budgets, the auto-RAG retrieval depth, and the ADR-0051
// thinking/window/session-budget/compaction policy. Every preset runs this same
// shape; the policy is one data file, not per-mode config.
type PipelinePolicy struct {
	MaxSteps         int
	MaxHistoryTokens int
	MaxRagTokens     int
	MaxMentionTokens int
	AutoRagTopK      int

	// Thinking is the default level (ADR-0051 §1); a session policy or a
	// per-turn Task.context override may replace it.
	Thinking ThinkingLevel
	// MaxThinkingTokens is the reasoning-token cap (ADR-0051 §5). Hitting it
	// labels the turn thinking-truncated; it is generous enough not to truncate
	// before a tool call.
	MaxThinkingTokens int
	// ReserveOutputTokens is the window gate's output reserve (ADR-0051 §6):
	// max_tokens plus a safety margin, held back from the context window.
	ReserveOutputTokens int
	// SessionBudgetSoftRatio is the soft threshold as a ratio of
	// Session.tokenBudget (ADR-0051 §7): crossing it labels a warning and the
	// turn proceeds; the hard threshold (the budget itself) refuses.
	SessionBudgetSoftRatio float64
	// Compaction is the metered history-compaction policy (ADR-0051 §8).
	Compaction CompactionPolicy
}
