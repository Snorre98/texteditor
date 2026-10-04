package pipeline

import (
	"errors"
	"testing"

	"texteditor/config"
	"texteditor/shared/dto"
)

// TestNewLoadsPolicy pins the shipped starting values (tuned in Phase F).
func TestNewLoadsPolicy(t *testing.T) {
	p, err := New()
	if err != nil {
		t.Fatal(err)
	}
	got := p.Policy()
	want := dto.PipelinePolicy{
		MaxSteps:         6,
		MaxHistoryTokens: 32000,
		MaxRagTokens:     16000,
		MaxMentionTokens: 16000,
		AutoRagTopK:      3,
	}
	if got != want {
		t.Fatalf("policy = %+v, want %+v", got, want)
	}
}

func TestParseRejectsInvalidPolicy(t *testing.T) {
	cases := []struct {
		name string
		data string
	}{
		{"missing field", `{"maxSteps":6,"maxHistoryTokens":1,"maxRagTokens":1,"maxMentionTokens":1}`},
		{"zero steps", `{"maxSteps":0,"maxHistoryTokens":1,"maxRagTokens":1,"maxMentionTokens":1,"autoRagTopK":1}`},
		{"zero top-k", `{"maxSteps":6,"maxHistoryTokens":1,"maxRagTokens":1,"maxMentionTokens":1,"autoRagTopK":0}`},
		{"negative budget", `{"maxSteps":6,"maxHistoryTokens":-1,"maxRagTokens":1,"maxMentionTokens":1,"autoRagTopK":1}`},
		{"unknown field", `{"maxSteps":6,"maxHistoryTokens":1,"maxRagTokens":1,"maxMentionTokens":1,"autoRagTopK":1,"extra":true}`},
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
	p, err := parse(config.PipelineSchema, []byte(
		`{"maxSteps":1,"maxHistoryTokens":0,"maxRagTokens":0,"maxMentionTokens":0,"autoRagTopK":1}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	if p.MaxHistoryTokens != 0 || p.MaxRagTokens != 0 || p.MaxMentionTokens != 0 {
		t.Fatalf("zero budgets should be valid: %+v", p)
	}
}
