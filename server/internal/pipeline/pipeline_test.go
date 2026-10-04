package pipeline

import (
	"errors"
	"strings"
	"testing"

	"texteditor/config"
	"texteditor/shared/dto"
)

// decValid is the shipped decision block (ADR-0053).
const decValid = `"decision":{"enabled":false,"model":"laya","gateThreshold":0.5,"maxCandidates":24,"maxHistoryTurns":4,"breadthTopK":{"none":0,"few":3,"many":8},"timeoutMs":3000}`

// baseValid is a minimal policy carrying every required field.
const baseValid = `{"maxSteps":1,"maxHistoryTokens":0,"maxRagTokens":0,"maxMentionTokens":0,"autoRagTopK":1,` +
	`"thinking":"auto","maxThinkingTokens":0,"reserveOutputTokens":0,"sessionBudgetSoftRatio":0.5,` +
	`"compaction":{"enabled":false,"triggerHistoryTokens":0,"keepRecentTurns":0},` + decValid + `}`

// TestNewLoadsPolicy pins the shipped starting values (tuned in Phase F).
func TestNewLoadsPolicy(t *testing.T) {
	p, err := New()
	if err != nil {
		t.Fatal(err)
	}
	got := p.Policy()
	want := dto.PipelinePolicy{
		MaxSteps:               6,
		MaxHistoryTokens:       32000,
		MaxRagTokens:           16000,
		MaxMentionTokens:       16000,
		AutoRagTopK:            3,
		Thinking:               dto.ThinkingAuto,
		MaxThinkingTokens:      4096,
		ReserveOutputTokens:    4096,
		SessionBudgetSoftRatio: 0.8,
		Compaction:             dto.CompactionPolicy{Enabled: true, TriggerHistoryTokens: 24000, KeepRecentTurns: 4},
		Decision: dto.DecisionPolicy{
			Enabled:         false,
			Model:           "laya",
			GateThreshold:   0.5,
			MaxCandidates:   24,
			MaxHistoryTurns: 4,
			BreadthTopK:     dto.DecisionBreadthTopK{None: 0, Few: 3, Many: 8},
			TimeoutMs:       3000,
		},
	}
	if got != want {
		t.Fatalf("policy = %+v, want %+v", got, want)
	}
}

func TestParseRejectsInvalidPolicy(t *testing.T) {
	// withDecision appends the required decision block so each case fails for
	// the reason it names, not for a missing decision block.
	withDecision := func(s string) string {
		if !strings.HasSuffix(s, "}") {
			return s
		}
		return strings.TrimSuffix(s, "}") + "," + decValid + "}"
	}
	cases := []struct {
		name string
		data string
	}{
		{"missing field", `{"maxSteps":6,"maxHistoryTokens":1,"maxRagTokens":1,"maxMentionTokens":1}`},
		{"zero steps", withDecision(`{"maxSteps":0,"maxHistoryTokens":1,"maxRagTokens":1,"maxMentionTokens":1,"autoRagTopK":1,"thinking":"auto","maxThinkingTokens":0,"reserveOutputTokens":0,"sessionBudgetSoftRatio":0.5,"compaction":{"enabled":false,"triggerHistoryTokens":0,"keepRecentTurns":0}}`)},
		{"zero top-k", withDecision(`{"maxSteps":6,"maxHistoryTokens":1,"maxRagTokens":1,"maxMentionTokens":1,"autoRagTopK":0,"thinking":"auto","maxThinkingTokens":0,"reserveOutputTokens":0,"sessionBudgetSoftRatio":0.5,"compaction":{"enabled":false,"triggerHistoryTokens":0,"keepRecentTurns":0}}`)},
		{"negative budget", withDecision(`{"maxSteps":6,"maxHistoryTokens":-1,"maxRagTokens":1,"maxMentionTokens":1,"autoRagTopK":1,"thinking":"auto","maxThinkingTokens":0,"reserveOutputTokens":0,"sessionBudgetSoftRatio":0.5,"compaction":{"enabled":false,"triggerHistoryTokens":0,"keepRecentTurns":0}}`)},
		{"bad thinking level", withDecision(`{"maxSteps":6,"maxHistoryTokens":1,"maxRagTokens":1,"maxMentionTokens":1,"autoRagTopK":1,"thinking":"maybe","maxThinkingTokens":0,"reserveOutputTokens":0,"sessionBudgetSoftRatio":0.5,"compaction":{"enabled":false,"triggerHistoryTokens":0,"keepRecentTurns":0}}`)},
		{"soft ratio out of range", withDecision(`{"maxSteps":6,"maxHistoryTokens":1,"maxRagTokens":1,"maxMentionTokens":1,"autoRagTopK":1,"thinking":"auto","maxThinkingTokens":0,"reserveOutputTokens":0,"sessionBudgetSoftRatio":2,"compaction":{"enabled":false,"triggerHistoryTokens":0,"keepRecentTurns":0}}`)},
		{"unknown field", withDecision(`{"maxSteps":6,"maxHistoryTokens":1,"maxRagTokens":1,"maxMentionTokens":1,"autoRagTopK":1,"thinking":"auto","maxThinkingTokens":0,"reserveOutputTokens":0,"sessionBudgetSoftRatio":0.5,"compaction":{"enabled":false,"triggerHistoryTokens":0,"keepRecentTurns":0},"extra":true}`)},
		{"missing decision", `{"maxSteps":6,"maxHistoryTokens":1,"maxRagTokens":1,"maxMentionTokens":1,"autoRagTopK":1,"thinking":"auto","maxThinkingTokens":0,"reserveOutputTokens":0,"sessionBudgetSoftRatio":0.5,"compaction":{"enabled":false,"triggerHistoryTokens":0,"keepRecentTurns":0}}`},
		{"bad gate threshold", `{"maxSteps":6,"maxHistoryTokens":1,"maxRagTokens":1,"maxMentionTokens":1,"autoRagTopK":1,"thinking":"auto","maxThinkingTokens":0,"reserveOutputTokens":0,"sessionBudgetSoftRatio":0.5,"compaction":{"enabled":false,"triggerHistoryTokens":0,"keepRecentTurns":0},"decision":{"enabled":false,"model":"laya","gateThreshold":2,"maxCandidates":24,"maxHistoryTurns":4,"breadthTopK":{"none":0,"few":3,"many":8},"timeoutMs":3000}}`},
		{"not an object", `[]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parse(config.PipelineSchema, []byte(tc.data))
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestParseAcceptsZeroBudgets(t *testing.T) {
	p, err := parse(config.PipelineSchema, []byte(baseValid))
	if err != nil {
		t.Fatal(err)
	}
	if p.MaxHistoryTokens != 0 || p.MaxRagTokens != 0 || p.MaxMentionTokens != 0 {
		t.Fatalf("zero budgets should be valid: %+v", p)
	}
	if p.Thinking != dto.ThinkingAuto {
		t.Fatalf("thinking = %q, want auto", p.Thinking)
	}
}
