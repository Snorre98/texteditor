package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
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

func TestSaveContextRoundTrip(t *testing.T) {
	s := newTestStore(t)
	sess, _ := s.Create("doc1", nil, "editor")

	snap := []byte(`{"turnId":"t1","messages":[{"component":"system"}]}`)
	if err := s.SaveContext("t1", sess.ID, snap); err != nil {
		t.Fatal(err)
	}
	got, err := s.TurnContext("t1")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(snap) {
		t.Fatalf("TurnContext = %s, want %s", got, snap)
	}

	// Re-saving the same turn upserts, not duplicates.
	if err := s.SaveContext("t1", sess.ID, []byte(`{"turnId":"t1","v":2}`)); err != nil {
		t.Fatal(err)
	}
	got, _ = s.TurnContext("t1")
	if string(got) != `{"turnId":"t1","v":2}` {
		t.Fatalf("TurnContext after upsert = %s", got)
	}

	if _, err := s.TurnContext("missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown turn: want ErrNotFound, got %v", err)
	}
}

func TestSaveContextRetentionKeepsNewest100(t *testing.T) {
	s := newTestStore(t)
	sess, _ := s.Create("doc1", nil, "editor")

	// Insert 105 snapshots for one session; retention keeps the newest 100.
	for i := 0; i < 105; i++ {
		if err := s.SaveContext("t-"+itoa(i), sess.ID, []byte(`{"i":`+itoa(i)+`}`)); err != nil {
			t.Fatal(err)
		}
	}
	// The oldest five (t-0..t-4) are pruned; the newest is retained.
	if _, err := s.TurnContext("t-0"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("oldest snapshot should be pruned, got %v", err)
	}
	if _, err := s.TurnContext("t-5"); err != nil {
		t.Fatalf("snapshot t-5 should be retained: %v", err)
	}
	if _, err := s.TurnContext("t-104"); err != nil {
		t.Fatalf("newest snapshot should be retained: %v", err)
	}
	// A second session's snapshots are unaffected by the first's retention.
	sess2, _ := s.Create("doc2", nil, "editor")
	if err := s.SaveContext("other", sess2.ID, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TurnContext("other"); err != nil {
		t.Fatalf("second session snapshot pruned: %v", err)
	}
}

// itoa is a tiny local int formatter (avoids importing strconv in the test).
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	p := len(b)
	for i > 0 {
		p--
		b[p] = byte('0' + i%10)
		i /= 10
	}
	return string(b[p:])
}

func TestContextPolicyRoundTrip(t *testing.T) {
	s := newTestStore(t)
	sess, err := s.Create("doc1", nil, "editor")
	if err != nil {
		t.Fatal(err)
	}
	if sess.ContextPolicy != nil {
		t.Fatalf("new session policy = %s, want none", sess.ContextPolicy)
	}
	policy := json.RawMessage(`{"pinned":[{"path":"/v/a.md","chunkKey":"/v/a.md#0"}],"autoRag":false}`)
	if err := s.SetContextPolicy(sess.ID, policy); err != nil {
		t.Fatal(err)
	}
	got, err := s.Resume(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(got.ContextPolicy) != string(policy) {
		t.Fatalf("Resume policy = %s, want %s", got.ContextPolicy, policy)
	}
	raw, err := s.ContextPolicy(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != string(policy) {
		t.Fatalf("ContextPolicy = %s, want %s", raw, policy)
	}
	list, err := s.ListByWorkspace()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || string(list[0].ContextPolicy) != string(policy) {
		t.Fatalf("ListByWorkspace policy = %+v", list)
	}
}

// TestContextPolicyReopenSurvives: the policy is durable across a store reopen
// (file-backed sessions.db), so it survives resume/adr-0026 §5.
func TestContextPolicyReopenSurvives(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.db")
	open := func() (*sql.DB, Interface) {
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		if err := sqlmigrate.Migrate(context.Background(), db, sessionsSchema); err != nil {
			t.Fatal(err)
		}
		return db, New(db)
	}
	db, st := open()
	sess, err := st.Create("doc1", nil, "editor")
	if err != nil {
		t.Fatal(err)
	}
	policy := json.RawMessage(`{"excluded":[{"path":"/v/b.md"}]}`)
	if err := st.SetContextPolicy(sess.ID, policy); err != nil {
		t.Fatal(err)
	}
	db.Close()

	db2, st2 := open()
	defer db2.Close()
	got, err := st2.Resume(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(got.ContextPolicy) != string(policy) {
		t.Fatalf("reopened policy = %s, want %s", got.ContextPolicy, policy)
	}
}

func TestContextPolicyInvalidIsTyped(t *testing.T) {
	s := newTestStore(t)
	sess, _ := s.Create("doc1", nil, "editor")
	if err := s.SetContextPolicy(sess.ID, json.RawMessage(`{`)); !errors.Is(err, ErrInvalidContextPolicy) {
		t.Fatalf("invalid policy error = %v, want ErrInvalidContextPolicy", err)
	}
}

func TestContextPolicyUnknownSession(t *testing.T) {
	s := newTestStore(t)
	if err := s.SetContextPolicy("nope", json.RawMessage(`{}`)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetContextPolicy unknown = %v, want ErrNotFound", err)
	}
	if _, err := s.ContextPolicy("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ContextPolicy unknown = %v, want ErrNotFound", err)
	}
}

// TestCompactHistoryReplacesOldestTurns: the oldest turns are replaced by one
// labeled summary message; the most recent keepRecentTurns survive (ADR-0051 §8).
func TestCompactHistoryReplacesOldestTurns(t *testing.T) {
	s := newTestStore(t)
	sess, err := s.Create("doc1", nil, "editor")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		if err := s.Append(sess.ID, dto.Message{Role: "user", Content: fmt.Sprintf("q%d", i)}); err != nil {
			t.Fatal(err)
		}
		if err := s.Append(sess.ID, dto.Message{Role: "assistant", Content: fmt.Sprintf("a%d", i)}); err != nil {
			t.Fatal(err)
		}
	}

	from, to, turns, err := s.CompactHistory(sess.ID, "the earlier gist", 2)
	if err != nil {
		t.Fatal(err)
	}
	if turns != 2 {
		t.Fatalf("turns = %d, want 2", turns)
	}
	if from == 0 || to == 0 {
		t.Fatalf("range = (%d,%d), want non-zero", from, to)
	}

	hist, err := s.History(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	// One summary + the two most recent turns (4 messages) = 5.
	if len(hist) != 5 {
		t.Fatalf("history length = %d, want 5: %+v", len(hist), hist)
	}
	if !strings.Contains(hist[0].Content, "Summary") || !strings.Contains(hist[0].Content, "the earlier gist") {
		t.Fatalf("first message is not the labeled summary: %q", hist[0].Content)
	}
	if hist[1].Content != "q2" {
		t.Fatalf("kept turn starts at %q, want q2", hist[1].Content)
	}

	// A second compaction with enough recent turns is a no-op.
	if _, _, n, err := s.CompactHistory(sess.ID, "again", 10); err != nil || n != 0 {
		t.Fatalf("no-op compaction = (%d,%v), want (0,nil)", n, err)
	}
}
