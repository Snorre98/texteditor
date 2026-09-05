package document

import (
	"regexp"
	"strings"

	"texteditor/shared/dto"
)

var (
	fenceLineRe     = regexp.MustCompile("^(`{3,}|~{3,})")
	headingMarkerRe = regexp.MustCompile(`^#{1,6}[ \t]+`)
	listMarkerRe    = regexp.MustCompile(`^([-*+]|\d+[.)])[ \t]+`)
	tableRuleRe     = regexp.MustCompile(`^\s*\|?[\s:|-]+\|?\s*$`)
)

// markdown.go owns the block <-> worktree mapping: the engine's canonical bytes
// live in the git worktree file as markdown (ADR-0020 §2), while the blocks table
// stores structure only (IDs, parent, kind, position — no text column). A block's
// Text is its full markdown source fragment (a heading keeps its "## " marker, a
// list item keeps its "- ", a code fence keeps its fence lines), so serializing
// the tree is a plain join and hashing Text is stable (ADR-0029 §3).

// parseBlocks splits canonical markdown into typed block source fragments,
// preserving document order. Each returned fragment is the block's full markdown
// source text (its canonical content).
func parseBlocks(source string) []struct {
	Kind dto.BlockKind
	Text string
} {
	if strings.TrimSpace(source) == "" {
		return nil
	}
	raw := splitTopLevel(source)
	out := make([]struct {
		Kind dto.BlockKind
		Text string
	}, 0, len(raw))
	for _, r := range raw {
		out = append(out, struct {
			Kind dto.BlockKind
			Text string
		}{classifyBlock(r), r})
	}
	return out
}

// splitTopLevel splits markdown source into block source fragments at blank-line
// boundaries, respecting fenced code blocks (a fence interior may contain blank
// lines and must stay intact).
func splitTopLevel(source string) []string {
	lines := strings.Split(source, "\n")
	var blocks []string
	cur := make([]string, 0, 4)
	inFence := false
	var fenceChar string
	flush := func() {
		if len(cur) == 0 {
			return
		}
		blocks = append(blocks, strings.Join(cur, "\n"))
		cur = make([]string, 0, 4)
	}
	for _, ln := range lines {
		if fenceLineRe.MatchString(ln) {
			c := ln[:1]
			if !inFence {
				inFence = true
				fenceChar = c
			} else if fenceChar == c {
				inFence = false
			}
			cur = append(cur, ln)
			continue
		}
		if !inFence && strings.TrimSpace(ln) == "" {
			if len(cur) > 0 {
				// A blank line ends the current block unless we're mid-fence.
				flush()
			}
			continue
		}
		cur = append(cur, ln)
	}
	flush()
	return blocks
}

// classifyBlock returns the BlockKind of a single block source fragment.
func classifyBlock(text string) dto.BlockKind {
	first := ""
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		first = text[:i]
	} else {
		first = text
	}
	t := strings.TrimLeft(first, " \t")
	if strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
		return dto.BlockKindCodeFence
	}
	if strings.HasPrefix(t, ">") {
		return dto.BlockKindBlockquote
	}
	if headingMarkerRe.MatchString(t) {
		return dto.BlockKindHeading
	}
	// A table has a separator row as its second line (|---|---|).
	if isTable(text) {
		return dto.BlockKindTable
	}
	if listMarkerRe.MatchString(t) {
		return dto.BlockKindListItem
	}
	return dto.BlockKindParagraph
}

func isTable(text string) bool {
	lines := strings.Split(text, "\n")
	for i, ln := range lines {
		if i == 0 {
			continue
		}
		if strings.Contains(ln, "|") && tableRuleRe.MatchString(ln) {
			return true
		}
		if strings.TrimSpace(ln) == "" {
			break
		}
	}
	return false
}

// serializeBlocks joins block source fragments back into canonical markdown.
func serializeBlocks(texts []string) string {
	return strings.Join(texts, "\n\n")
}
