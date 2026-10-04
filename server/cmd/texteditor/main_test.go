package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"

	"texteditor/internal/document"
	"texteditor/internal/textformatter"
	"texteditor/shared/dto"
)

func TestEnvOr(t *testing.T) {
	key := "TEXTEDITOR_TEST_ENVOR"
	t.Cleanup(func() { os.Unsetenv(key) })

	if got := envOr(key, "default"); got != "default" {
		t.Fatalf("envOr(unset) = %q, want default", got)
	}
	os.Setenv(key, "custom")
	if got := envOr(key, "default"); got != "custom" {
		t.Fatalf("envOr(set) = %q, want custom", got)
	}
}

func TestEnvInt(t *testing.T) {
	key := "TEXTEDITOR_TEST_ENVINT"
	t.Cleanup(func() { os.Unsetenv(key) })

	if got := envInt(key, 42); got != 42 {
		t.Fatalf("envInt(unset) = %d, want 42", got)
	}
	os.Setenv(key, "9100")
	if got := envInt(key, 42); got != 9100 {
		t.Fatalf("envInt(set) = %d, want 9100", got)
	}
	os.Setenv(key, "not-a-number")
	if got := envInt(key, 42); got != 42 {
		t.Fatalf("envInt(garbage) = %d, want fallback 42", got)
	}
}

func TestSplitList(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"tauri://localhost", []string{"tauri://localhost"}},
		{"tauri://localhost, http://localhost:5173", []string{"tauri://localhost", "http://localhost:5173"}},
		{" a , , b ", []string{"a", "b"}},
	}
	for _, c := range cases {
		got := splitList(c.in)
		if len(got) != len(c.want) {
			t.Fatalf("splitList(%q) = %v, want %v", c.in, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("splitList(%q) = %v, want %v", c.in, got, c.want)
			}
		}
	}
}

func TestBindListenerDynamic(t *testing.T) {
	ln, baseURL, err := bindListener("127.0.0.1", 0)
	if err != nil {
		t.Fatalf("bindListener(dynamic): %v", err)
	}
	defer ln.Close()

	host, port, err := net.SplitHostPort(strings.TrimPrefix(baseURL, "http://"))
	if err != nil {
		t.Fatalf("baseURL %q not host:port: %v", baseURL, err)
	}
	if host != "127.0.0.1" {
		t.Fatalf("baseURL host = %q, want 127.0.0.1", host)
	}
	if n, _ := strconv.Atoi(port); n == 0 {
		t.Fatalf("dynamic port resolved to 0, want an OS-assigned free port")
	}
}

func TestBindListenerFixed(t *testing.T) {
	// Find a free port, then bind it fixed.
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("probe listen: %v", err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	probe.Close()

	ln, baseURL, err := bindListener("127.0.0.1", port)
	if err != nil {
		t.Fatalf("bindListener(fixed %d): %v", port, err)
	}
	defer ln.Close()
	if want := "http://127.0.0.1:" + strconv.Itoa(port); baseURL != want {
		t.Fatalf("baseURL = %q, want %q", baseURL, want)
	}
}

// captureDoc is a document.Interface stub recording the last ApplyEdit. The
// embedded nil interface satisfies the unused methods.
type captureDoc struct {
	document.Interface

	blocks   []dto.Block
	applyErr error

	mu   sync.Mutex
	edit dto.BlockEdit
}

func (c *captureDoc) Blocks(string) ([]dto.Block, error) { return c.blocks, nil }

func (c *captureDoc) ApplyEdit(_ context.Context, _ string, edit dto.BlockEdit) (dto.Revision, error) {
	c.mu.Lock()
	c.edit = edit
	c.mu.Unlock()
	if c.applyErr != nil {
		return dto.Revision{}, c.applyErr
	}
	return dto.Revision{ID: "r1", Message: "candidate staged", Timestamp: 1}, nil
}

func (c *captureDoc) lastEdit() dto.BlockEdit {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.edit
}

// TestEditMarkdownHandlerPassesBaseHashGuardAndMode covers ADR-0047 §6: the
// model's echoed base hash becomes a guard on the target block, and the
// loop-injected preset rides along for the derived commit message.
func TestEditMarkdownHandlerPassesBaseHashGuardAndMode(t *testing.T) {
	doc := &captureDoc{blocks: []dto.Block{
		{ID: "b1", Kind: dto.BlockKindParagraph, Text: "hello", Hash: "abcd1234"},
	}}
	h := editMarkdownHandler(doc, textformatter.New())

	out, err := h(context.Background(), json.RawMessage(`{"blockId":"b1","text":"new text","baseHash":"abcd1234","documentId":"d1","modeName":"proofreader"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"ok":true`) {
		t.Fatalf("result = %s, want ok", out)
	}
	edit := doc.lastEdit()
	if edit.Mode != "proofreader" {
		t.Fatalf("mode = %q, want proofreader", edit.Mode)
	}
	if len(edit.Guards) != 1 || edit.Guards[0].BlockID != "b1" || edit.Guards[0].Hash != "abcd1234" {
		t.Fatalf("guards = %+v, want the target block base-hash guard", edit.Guards)
	}
}

// TestEditMarkdownHandlerReportsGuardFailed covers the structured retry result:
// a stale echo surfaces as guard-failed, never a silent write.
func TestEditMarkdownHandlerReportsGuardFailed(t *testing.T) {
	doc := &captureDoc{
		blocks:   []dto.Block{{ID: "b1", Kind: dto.BlockKindParagraph, Text: "hello", Hash: "now"}},
		applyErr: errors.Join(document.ErrGuardFailed, errors.New("b1: content changed")),
	}
	h := editMarkdownHandler(doc, textformatter.New())

	out, err := h(context.Background(), json.RawMessage(`{"blockId":"b1","text":"stale text","baseHash":"then","documentId":"d1"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"ok":false`) || !strings.Contains(string(out), `"error":"guard-failed"`) {
		t.Fatalf("result = %s, want structured guard-failed", out)
	}
}
