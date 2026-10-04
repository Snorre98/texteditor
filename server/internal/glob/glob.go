// Package glob is a pure leaf implementing the corpus scope matcher (ADR-0049
// §3): `*` and `?` within one path segment, `[class]` via path.Match, and `**`
// matching any number of path segments (including zero). Patterns and names are
// slash-separated relative paths. It adds no dependency; path.Match alone cannot
// express `**`.
package glob

import (
	"path"
	"strings"
)

// Match reports whether the slash-separated name matches pattern.
//
// Examples: `**/*.md` matches `a.md` and `chapters/one.md`; `notes/**` matches
// `notes/a.md` and `notes`; `*.md` matches only top-level markdown.
func Match(pattern, name string) bool {
	pattern = strings.TrimPrefix(pattern, "./")
	name = strings.TrimPrefix(name, "./")
	return matchSegments(split(pattern), split(name))
}

// MatchAny reports whether name matches any pattern. An empty pattern list
// matches nothing (the caller applies defaults).
func MatchAny(patterns []string, name string) bool {
	for _, p := range patterns {
		if Match(p, name) {
			return true
		}
	}
	return false
}

// Validate reports whether a pattern is syntactically usable. It only checks
// path.Match syntax on the non-`**` segments.
func Validate(pattern string) error {
	for _, seg := range split(pattern) {
		if seg == "**" {
			continue
		}
		if _, err := path.Match(seg, ""); err != nil {
			return err
		}
	}
	return nil
}

func split(p string) []string {
	p = strings.Trim(p, "/")
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

func matchSegments(pattern, name []string) bool {
	if len(pattern) == 0 {
		return len(name) == 0
	}
	if pattern[0] == "**" {
		// `**` consumes zero or more segments.
		for i := 0; i <= len(name); i++ {
			if matchSegments(pattern[1:], name[i:]) {
				return true
			}
		}
		return false
	}
	if len(name) == 0 {
		return false
	}
	ok, err := path.Match(pattern[0], name[0])
	if err != nil || !ok {
		return false
	}
	return matchSegments(pattern[1:], name[1:])
}
