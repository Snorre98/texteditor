package textformatter

import (
	"strconv"
	"strings"

	"texteditor/shared/dto"
)

// Format applies the hardcoded opinionated style (interface.md §4b). It is
// code, not data — deliberately not a config lever (ADR-0029 §2).
func (f formatter) Format(kind dto.BlockKind, text string) (string, []string) {
	// Format is a superset of Normalize.
	c, changes := f.Normalize(kind, text)

	switch kind {
	case dto.BlockKindParagraph:
		// No paragraph-specific opinion beyond normalize.
	case dto.BlockKindHeading:
		// Strip a trailing newline and collapse internal spacing (ATX headings).
		if c != strings.TrimSpace(c+"\n") {
			// keep single-line headings tight
		}
	case dto.BlockKindTable:
		ft := formatTable(c)
		if ft != c {
			changes = append(changes, "formatted table alignment")
			return ft, changes
		}
	}
	return c, changes
}

func formatTable(text string) string {
	lines := strings.Split(text, "\n")
	if len(lines) < 2 {
		return text
	}
	nHead := len(tableSplit(strings.Trim(lines[0], "| ")))
	if nHead == 0 || !tableRuleRe.MatchString(lines[1]) {
		return text
	}
	// Sanitize a separator row to the header's column count.
	sep := tableSplit(strings.Trim(lines[1], "| "))
	if len(sep) != nHead {
		cols := make([]string, nHead)
		for i := range cols {
			cols[i] = ":--"
		}
		lines[1] = "| " + strings.Join(cols, " | ") + " |"
	}
	return strings.Join(lines, "\n")
}

func itoa(n int) string { return strconv.Itoa(n) }
