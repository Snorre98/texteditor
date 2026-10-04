package glob

import "testing"

func TestMatch(t *testing.T) {
	cases := []struct {
		pattern string
		name    string
		want    bool
	}{
		{"**/*.md", "a.md", true},
		{"**/*.md", "chapters/one.md", true},
		{"**/*.md", "chapters/one.txt", false},
		{"*.md", "a.md", true},
		{"*.md", "chapters/one.md", false},
		{"notes/**", "notes/a.md", true},
		{"notes/**", "notes", true},
		{"notes/**", "notes2/a.md", false},
		{"notes/*.md", "notes/a.md", true},
		{"notes/*.md", "notes/sub/a.md", false},
		{"**/drafts/**", "x/y/drafts/a.md", true},
		{"**/drafts/**", "x/y/final/a.md", false},
		{"?.md", "a.md", true},
		{"?.md", "ab.md", false},
		{"[ab].md", "a.md", true},
		{"[ab].md", "c.md", false},
		{"**", "anything/deep/here.md", true},
	}
	for _, c := range cases {
		if got := Match(c.pattern, c.name); got != c.want {
			t.Errorf("Match(%q, %q) = %v, want %v", c.pattern, c.name, got, c.want)
		}
	}
}

func TestMatchAny(t *testing.T) {
	patterns := []string{"**/*.md", "**/*.txt"}
	if !MatchAny(patterns, "a/b/c.txt") {
		t.Fatal("expected txt match")
	}
	if MatchAny(patterns, "a/b/c.pdf") {
		t.Fatal("unexpected pdf match")
	}
	if MatchAny(nil, "a.md") {
		t.Fatal("empty pattern list must match nothing")
	}
}

func TestValidate(t *testing.T) {
	if err := Validate("**/*.md"); err != nil {
		t.Fatal(err)
	}
	if err := Validate("[unterminated"); err == nil {
		t.Fatal("expected invalid class error")
	}
}
