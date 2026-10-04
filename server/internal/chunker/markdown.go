package chunker

import (
	"regexp"
	"strings"

	"texteditor/shared/dto"
)

// headingRe matches an ATX markdown heading (`#`..`######` + space + text).
var headingRe = regexp.MustCompile(`^(#{1,6})\s+(.*)$`)

// fenceRe matches a fenced-code delimiter (``` or ~~~).
var fenceRe = regexp.MustCompile("^(```|~~~)")

// ChunkMarkdown splits raw markdown into heading-aware, paragraph-aligned
// chunks (ADR-0044 §3, ADR-0049 §4). It is a pure function: corpus files are
// indexed path-keyed with no document row, so the Retriever reads the file and
// calls this directly.
//
// Boundaries: a heading always starts a new chunk and labels every following
// chunk with its heading stack ("Parent > Child"); blank lines end paragraphs;
// a paragraph is never split, and a single oversized paragraph becomes its own
// chunk (mirroring Chunk). Fenced code is kept verbatim and never parsed for
// headings. The returned chunks carry Heading (and Text) only; the caller
// assigns Path/ChunkKey.
func ChunkMarkdown(raw string, maxTokens int) ([]dto.Chunk, error) {
	if maxTokens <= 0 {
		return nil, ErrZeroTokens
	}
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	lines := strings.Split(raw, "\n")

	var (
		chunks    []dto.Chunk
		stack     []string
		cur       []string
		curTokens int
		curHead   string
		inFence   bool
		para      []string
	)

	flush := func() {
		if curTokens == 0 {
			return
		}
		chunks = append(chunks, dto.Chunk{
			Text:    strings.Join(cur, "\n\n"),
			Heading: curHead,
		})
		cur = nil
		curTokens = 0
	}
	addText := func(t string) {
		toks := estimate(t)
		if curTokens > 0 && curTokens+toks > maxTokens {
			flush()
		}
		cur = append(cur, t)
		curTokens += toks
	}
	flushPara := func() {
		if len(para) == 0 {
			return
		}
		addText(strings.Join(para, "\n"))
		para = nil
	}

	for _, line := range lines {
		if inFence {
			para = append(para, line)
			if fenceRe.MatchString(line) {
				inFence = false
			}
			continue
		}
		if fenceRe.MatchString(line) {
			inFence = true
			para = append(para, line)
			continue
		}
		if m := headingRe.FindStringSubmatch(line); m != nil {
			flushPara()
			flush() // heading boundary: the previous section ends here
			level := len(m[1])
			text := strings.TrimSpace(m[2])
			if level <= len(stack) {
				stack = stack[:level-1]
			} else {
				for len(stack) < level-1 {
					stack = append(stack, "")
				}
			}
			stack = append(stack, text)
			curHead = strings.Join(nonEmpty(stack), " > ")
			addText(line)
			continue
		}
		if strings.TrimSpace(line) == "" {
			flushPara()
			continue
		}
		para = append(para, line)
	}
	flushPara()
	flush()
	return chunks, nil
}

// nonEmpty drops the placeholder levels inserted when a heading jumps a level
// (e.g. `# A` then `### C` → "A > C").
func nonEmpty(parts []string) []string {
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
