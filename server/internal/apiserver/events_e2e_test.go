package apiserver

// E2E test for the non-turn liveness feed (ADR-0052 §4): GET /events delivers
// workspace-scoped feed events, filters out turn events, and coexists with
// /turn's per-turn stream.

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"texteditor/shared/dto"
)

func TestEventsFeedDeliversScopedEventsAndExcludesTurn(t *testing.T) {
	srv, bus := newTestServer(t)
	ts := httptest.NewServer(http.HandlerFunc(srv.ServeHTTP))
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/events?workspaceId=ws1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type = %q, want text/event-stream", ct)
	}

	// A matching session event, an excluded turn event, and a different
	// workspace's event. Only the first must be delivered.
	bus.Emit(dto.Event{Type: dto.EventSession, WorkspaceID: "ws1",
		Data: json.RawMessage(`{"kind":"created","workspaceId":"ws1","session":{"id":"s1","documentId":"d1"}}`)})
	bus.Emit(dto.Event{TurnID: "t1", Type: "token", Data: json.RawMessage(`{"text":"x"}`)})
	bus.Emit(dto.Event{Type: dto.EventSession, WorkspaceID: "other",
		Data: json.RawMessage(`{"kind":"created","workspaceId":"other","session":{"id":"s2","documentId":"d2"}}`)})

	var lines []string
	done := make(chan struct{})
	go func() {
		defer close(done)
		reader := bufio.NewReader(resp.Body)
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			lines = append(lines, strings.TrimSpace(line))
			if strings.Contains(line, `"s1"`) {
				return
			}
		}
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for a feed event")
	}

	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "event: "+dto.EventSession) {
		t.Fatalf("feed did not deliver the session event: %q", joined)
	}
	if strings.Contains(joined, "event: token") {
		t.Fatalf("turn event leaked into the feed: %q", joined)
	}
	if strings.Contains(joined, `"s2"`) {
		t.Fatalf("event from another workspace leaked into the feed: %q", joined)
	}
}
