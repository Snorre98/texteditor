package session

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	_ "modernc.org/sqlite"

	"texteditor/internal/sqlmigrate"
	"texteditor/shared/dto"
)

func newTestStore(t *testing.T) Interface {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := sqlmigrate.Migrate(context.Background(), db, sessionsSchema); err != nil {
		t.Fatal(err)
	}
	return New(db)
}

func blockPtr(s string) *string { return &s }

func TestCreateResumeReopenSameSession(t *testing.T) {
	s := newTestStore(t)

	anchor := "block-P"
	s1, err := s.Create("doc1", blockPtr(anchor), "proofreader")
	if err != nil {
		t.Fatal(err)
	}
	if s1.AnchorBlockID == nil || *s1.AnchorBlockID != anchor {
		t.Fatalf("anchor = %v, want block-P", s1.AnchorBlockID)
	}

	// Re-anchoring the same block reopens the same session.
	s2, err := s.Create("doc1", blockPtr(anchor), "proofreader")
	if err != nil {
		t.Fatal(err)
	}
	if s2.ID != s1.ID {
		t.Fatalf("resume-reopen id %s != %s", s2.ID, s1.ID)
	}

	// A fresh block mints a new session.
	s3, err := s.Create("doc1", blockPtr("block-Q"), "proofreader")
	if err != nil {
		t.Fatal(err)
	}
	if s3.ID == s1.ID {
		t.Fatal("new anchor must mint a new session")
	}
}

func TestUnanchoredSession(t *testing.T) {
	s := newTestStore(t)
	s1, err := s.Create("doc1", nil, "editor")
	if err != nil {
		t.Fatal(err)
	}
	if s1.AnchorBlockID != nil {
		t.Fatalf("doc-level session should have nil anchor, got %v", *s1.AnchorBlockID)
	}
	// A second doc-level chat reopens the same unanchored session.
	s2, err := s.Create("doc1", nil, "editor")
	if err != nil {
		t.Fatal(err)
	}
	if s2.ID != s1.ID {
		t.Fatal("doc-level chat should resume")
	}
}

func TestListByDocument(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Create("doc1", nil, "editor"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create("doc1", blockPtr("a"), "editor"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create("doc1", blockPtr("b"), "editor"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create("doc2", nil, "editor"); err != nil {
		t.Fatal(err)
	}
	sessions, err := s.ListByDocument("doc1")
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 3 {
		t.Fatalf("sessions = %d, want 3", len(sessions))
	}
}

func TestAppendHistory(t *testing.T) {
	s := newTestStore(t)
	sess, _ := s.Create("doc1", nil, "editor")

	if err := s.Append(sess.ID, dto.Message{Role: "user", Content: "hi"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Append(sess.ID, dto.Message{Role: "assistant", Content: "hello"}); err != nil {
		t.Fatal(err)
	}

	hist, err := s.History(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 2 {
		t.Fatalf("history = %d, want 2", len(hist))
	}
	if hist[0].Role != "user" || hist[1].Role != "assistant" {
		t.Fatalf("history order wrong: %+v", hist)
	}

	// Resume returns the session; history persists.
	resumed, err := s.Resume(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.ID != sess.ID {
		t.Fatal("resume mismatch")
	}
	resumedHist, _ := s.History(resumed.ID)
	if len(resumedHist) != 2 {
		t.Fatalf("resumed history = %d, want 2", len(resumedHist))
	}
}

func TestResumeNotFound(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Resume("missing")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}
