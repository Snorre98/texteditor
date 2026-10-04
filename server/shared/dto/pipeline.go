package dto

// PipelinePolicy is the one global turn pipeline policy (ADR-0045 §3): the step
// cap, the context budgets, and the auto-RAG retrieval depth. Every preset runs
// this same shape; the policy is one data file, not per-mode config.
type PipelinePolicy struct {
	MaxSteps         int
	MaxHistoryTokens int
	MaxRagTokens     int
	MaxMentionTokens int
	AutoRagTopK      int
}
