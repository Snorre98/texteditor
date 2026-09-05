package textformatter

import (
	"regexp"
	"strings"

	"texteditor/shared/dto"
)

type formatter struct{}

// New returns the default TextFormatter implementation.
func New() Interface { return formatter{} }

var (
	// A markdown table separator row aligns (| --- | --- |) or unaligned (|---|).
	tableRuleRe       = regexp.MustCompile(`^\s*\|?[\s:|-]+\|?\s*$`)
	trailingWSRe      = regexp.MustCompile(`[ \t]+$`)
	multiBlankRe      = regexp.MustCompile(`\n{3,}`)
	leadingBlankRe    = regexp.MustCompile(`^\s+`)
	trailingBlankRe   = regexp.MustCompile(`\s+$`)
	fenceLineRe       = regexp.MustCompile("^(`{3,}|~{3,})")
	listIndentRe      = regexp.MustCompile(`^([ \t]*)([-*+]|\d+[.)])\s+`)
	listTaskRe        = regexp.MustCompile(`^([ \t]*)([-*+])[ \t]+(\[([ xX]?)\])[ \t]+`)
	tableSplitRe      = regexp.MustCompile(`\s*\|\s*`)
)

// Normalize returns semantic-preserving canonical whitespace.
func (formatter) Normalize(kind dto.BlockKind, text string) (string, []string) {
	var changes []string

	// Uniform newlines always.
	if strings.Contains(text, "\r\n") {
		text = strings.ReplaceAll(text, "\r\n", "\n")
		changes = append(changes, "normalized CRLF to LF")
	}
	if strings.Contains(text, "\r") {
		text = strings.ReplaceAll(text, "\r", "\n")
		changes = append(changes, "normalized CR to LF")
	}

	// Per-line structural normalization; never touch code fence interiors.
	lines := strings.Split(text, "\n")
	inFence := false
	var fenceChar string
	out := make([]string, 0, len(lines))
	for _, ln := range lines {
		if m := fenceLineRe.FindString(ln); m != "" {
			// A fence toggles in/out of a code block, unless unbalanced.
			if !inFence {
				inFence = true
				fenceChar = m[:1]
			} else if fenceChar == m[:1] {
				inFence = false
			}
			// Fence lines themselves: strip trailing whitespace.
			if trailingWSRe.MatchString(ln) {
				ln = trailingWSRe.ReplaceAllString(ln, "")
				changes = append(changes, "stripped trailing whitespace on fence line")
			}
			out = append(out, ln)
			continue
		}
		if inFence {
			out = append(out, ln)
			continue
		}
		if trailingWSRe.MatchString(ln) {
			ln = trailingWSRe.ReplaceAllString(ln, "")
			changes = append(changes, "stripped trailing whitespace")
		}
		out = append(out, ln)
	}
	text = strings.Join(out, "\n")

	// Collapse 3+ blank lines to 2 (a single gap).
	if multiBlankRe.MatchString(text) {
		text = multiBlankRe.ReplaceAllString(text, "\n\n")
		changes = append(changes, "collapsed multiple blank lines")
	}
	// Write normal form (no trailing newline).
	text = strings.TrimSuffix(text, "\n")
	if kind == dto.BlockKindTable {
		n := normalizeTable(text)
		if n != text {
			changes = append(changes, "aligned table pipes")
			text = n
		}
	}
	return text, changes
}

func normalizeTable(text string) string {
	lines := strings.Split(text, "\n")
	for i, ln := range lines {
		if i == 0 {
			if strings.HasPrefix(ln, "|") {
				lines[i] = strings.TrimSpace(ln)
			}
			continue
		}
		if tableRuleRe.MatchString(ln) {
			lines[i] = strings.TrimSpace(ln)
			continue
		}
		if strings.Contains(ln, "|") {
			lines[i] = strings.TrimSpace(ln)
		}
	}
	return strings.Join(lines, "\n")
}

// Validate returns structural-integrity issues (interface.md §4b).
func (formatter) Validate(kind dto.BlockKind, text string) []dto.TextFormatterIssue {
	var issues []dto.TextFormatterIssue
	lines := strings.Split(text, "\n")

	switch kind {
	case dto.BlockKindCodeFence:
		var fenceChar string
		open := 0
		for i, ln := range lines {
			m := fenceLineRe.FindString(ln)
			if m == "" {
				continue
			}
			c := m[:1]
			if open == 0 {
				open = 1
				fenceChar = c
			} else if fenceChar == c {
				open = 0
			} else {
				issues = append(issues, dto.TextFormatterIssue{
					Line: i + 1, Message: "closing fence char does not match opening fence",
				})
			}
		}
		if open == 1 {
			issues = append(issues, dto.TextFormatterIssue{Line: len(lines), Message: "unclosed code fence"})
		}
	case dto.BlockKindTable:
		if len(lines) < 2 {
			issues = append(issues, dto.TextFormatterIssue{Line: 1, Message: "table needs a header row and a separator row"})
			return issues
		}
		if !tableRuleRe.MatchString(lines[1]) {
			issues = append(issues, dto.TextFormatterIssue{Line: 2, Message: "missing separator row under the header"})
			return issues
		}
		nHead := len(tableSplit(strings.Trim(lines[0], "| ")))
		nSep := len(tableSplit(strings.Trim(lines[1], "| ")))
		if nHead != nSep {
			issues = append(issues, dto.TextFormatterIssue{
				Line:    2,
				Message: "separator row has " + itoa(nSep) + " columns, header has " + itoa(nHead),
			})
		}
		for i, ln := range lines[2:] {
			if !strings.Contains(ln, "|") {
				continue
			}
			nCol := len(tableSplit(strings.Trim(ln, "| ")))
			if nCol != nHead {
				issues = append(issues, dto.TextFormatterIssue{
					Line:    i + 3,
					Message: "row has " + itoa(nCol) + " columns, header has " + itoa(nHead),
				})
			}
		}
	default:
		// paragraph, heading, list, blockquote have no hard structural invariant.
	}
	return issues
}

func tableSplit(s string) []string {
	parts := tableSplitRe.Split(strings.Trim(s, "| "), -1)
	if len(parts) == 1 && parts[0] == "" {
		return nil
	}
	// Trim leading/trailing empties from a "| a | b |" split.
	for len(parts) > 0 && parts[0] == "" {
		parts = parts[1:]
	}
	return parts
}
