// Package textformatter owns formatting: the model never reproduces bytes
// (ADR-0029). It is a pure, deterministic leaf with a hardcoded opinionated
// style. The three ops hook three boundaries:
//
//	Normalize — ApplyEdit (model boundary), always; the candidate is always canonical
//	Validate  — the edit-tool handler, pre-flight; issues reach the model
//	Format    — Commit (accept) + autosave (save); the persisted doc is formatted
package textformatter

import "texteditor/shared/dto"

// Interface is the TextFormatter public API (interface.md §4b).
type Interface interface {
	Normalize(kind dto.BlockKind, text string) (canonical string, changes []string)
	Validate(kind dto.BlockKind, text string) []dto.TextFormatterIssue
	Format(kind dto.BlockKind, text string) (formatted string, changes []string)
}
