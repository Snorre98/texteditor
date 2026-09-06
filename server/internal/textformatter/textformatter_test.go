package textformatter

import (
	"testing"

	"texteditor/shared/dto"
)

func TestNormalizeSemanticWhitespace(t *testing.T) {
	f := New()

	cases := []struct{ name, in, want string }{
		{"crlf", "a\r\nb", "a\nb"},
		{"trailing ws", "a b   \n", "a b"},
		{"collapse blanks", "a\n\n\n\nb", "a\n\nb"},
	}
	for _, c := range cases {
		got, _ := f.Normalize(dto.BlockKindParagraph, c.in)
		if got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

func TestNormalizePreservesCodeFenceInterior(t *testing.T) {
	f := New()
	in := "```\n  indented   \nstill\n```"
	got, _ := f.Normalize(dto.BlockKindCodeFence, in)
	want := "```\n  indented   \nstill\n```"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestNormalizeIsIdempotentAndReportsChanges(t *testing.T) {
	f := New()
	in := "a  \r\n\r\n\r\nb  \r\n"
	first, changes := f.Normalize(dto.BlockKindParagraph, in)
	if len(changes) == 0 {
		t.Error("expected at least one change reported")
	}
	second, changes2 := f.Normalize(dto.BlockKindParagraph, first)
	if second != first {
		t.Errorf("not idempotent: %q != %q", second, first)
	}
	if len(changes2) != 0 {
		t.Errorf("idempotent pass reported changes: %v", changes2)
	}
}

func TestValidateTableColumnCount(t *testing.T) {
	f := New()
	// Header 2 cols, separator 1 col.
	in := "| a | b |\n| --- |"
	issues := f.Validate(dto.BlockKindTable, in)
	found := false
	for _, is := range issues {
		if is.Line == 2 {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a separator-column issue on line 2, got %v", issues)
	}

	// Valid table → no issues.
	valid := "| a | b |\n| --- | --- |\n| 1 | 2 |"
	if got := f.Validate(dto.BlockKindTable, valid); len(got) != 0 {
		t.Errorf("valid table reported issues: %v", got)
	}
}

func TestValidateUnclosedFence(t *testing.T) {
	f := New()
	issues := f.Validate(dto.BlockKindCodeFence, "```\nbody")
	found := false
	for _, is := range issues {
		if is.Message == "unclosed code fence" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected unclosed fence issue, got %v", issues)
	}
}

func TestFormatRunsOnCommitSemantics(t *testing.T) {
	f := New()
	// A table with a 2-col header but a 3-col separator (all dashes) is
	// corrected on Format to the header's column count.
	in := "| a | b |\n| :-- | :-- | :-- |"
	got, changes := f.Format(dto.BlockKindTable, in)
	if len(changes) == 0 {
		t.Errorf("expected a change on malformed separator, got none; out=%q", got)
	}
	want := "| a | b |\n| :-- | :-- |"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestNormalizeSubsetOfFormat(t *testing.T) {
	f := New()
	kind := dto.BlockKindParagraph
	in := "a  \r\n\r\n\r\nb  "
	n, _ := f.Normalize(kind, in)
	fo, _ := f.Format(kind, in)
	// Normalize output must be a prefix-normal-form that Format also reaches.
	if n != fo {
		// For a paragraph Format == Normalize; assert equality directly.
		t.Errorf("paragraph: Normalize=%q Format=%q should match", n, fo)
	}
}
