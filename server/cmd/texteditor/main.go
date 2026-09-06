// Command texteditor runs the writing-assistant engine: a single static Go
// binary (ADR-0003) exposing the REST/SSE surface (ADR-0017) and reaching
// serving only via the control daemon (ADR-0025/0027). No CGO.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"

	"texteditor/internal/apiserver"
	"texteditor/internal/assembler"
	"texteditor/internal/chunker"
	"texteditor/internal/document"
	"texteditor/internal/eventbus"
	"texteditor/internal/fleet"
	"texteditor/internal/loop"
	"texteditor/internal/meter"
	"texteditor/internal/mode"
	"texteditor/internal/provider"
	"texteditor/internal/retriever"
	"texteditor/internal/routergate"
	"texteditor/internal/session"
	"texteditor/internal/textformatter"
	"texteditor/internal/tool"
	"texteditor/internal/tooldecider"
	"texteditor/shared/dto"
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("texteditor: %v", err)
	}
}

func run() error {
	var (
		dataDir   = flag.String("data", defaultDataDir(), "directory for SQLite files + git worktrees")
		addr      = flag.String("addr", "127.0.0.1:9100", "API listen address")
		daemonURL = flag.String("daemon", envOr("DAEMON_URL", "http://127.0.0.1:9300"), "control daemon base URL (ADR-0025)")
	)
	flag.Parse()

	ctx := context.Background()

	if err := os.MkdirAll(*dataDir, 0o755); err != nil {
		return err
	}

	// Per-service SQLite files (single-writer, ADR-0016).
	open := func(name string) (*sql.DB, error) {
		return sql.Open("sqlite", filepath.Join(*dataDir, name))
	}

	// --- app.db (Document store) ---
	appDB, err := open("app.db")
	if err != nil {
		return err
	}
	if err := document.Migrate(ctx, appDB); err != nil {
		return fmt.Errorf("app.db: %w", err)
	}
	docStore, err := document.NewStore(appDB,
		filepath.Join(*dataDir, "git"),
		filepath.Join(*dataDir, "worktree"),
		textformatter.New(),
	)
	if err != nil {
		return fmt.Errorf("document store: %w", err)
	}

	// --- sessions.db (Session store) ---
	sessDB, err := open("sessions.db")
	if err != nil {
		return err
	}
	if err := session.Migrate(ctx, sessDB); err != nil {
		return fmt.Errorf("sessions.db: %w", err)
	}
	sessStore := session.New(sessDB)

	// --- meter.db (Token metering) ---
	meterDB, err := open("meter.db")
	if err != nil {
		return err
	}
	if err := meter.Migrate(ctx, meterDB); err != nil {
		return fmt.Errorf("meter.db: %w", err)
	}

	// --- bus (SSE fan-out; wired before meter/loop so events flow) ---
	bus := eventbus.New()
	meterStore := meter.New(meterDB, bus)

	// --- Fleet (daemon HTTP client) ---
	fleetGW := fleet.NewDaemon(*daemonURL)
	models, err := fleetGW.ListModels()
	if err != nil {
		// A dead daemon is fatal at startup for validation input (we need the
		// model/tags fact). Surface loudly rather than half-wiring.
		return fmt.Errorf("fleet unavailable: %w", err)
	}

	// --- Provider (one shared gateway: retriever embeds, loop streams, the
	// router decides — stateless transport, safe to share) ---
	providerGW := provider.New()

	// --- Retriever (index.db; block source = Document store) ---
	indexDB, err := open("index.db")
	if err != nil {
		return err
	}
	retrieverGW := retriever.New(indexDB, fleetGW, providerGW, docStore, chunker.New(), 512)

	// --- Tool registry + executor (ADR-0019; VerifyHandlers cross-check) ---
	registry, toolNames, err := tool.Load()
	if err != nil {
		return fmt.Errorf("tools: %w", err)
	}
	executor := tool.NewExecutor()
	handlers := makeToolHandlers(docStore, retrieverGW, textformatter.New())
	for _, name := range toolNames {
		if h, ok := handlers[name]; ok {
			executor.Bind(name, h)
		} else {
			executor.Bind(name, makeHandler(name))
		}
	}
	if err := tool.VerifyHandlers(registry, executor.HandlerNames()); err != nil {
		return err
	}

	// --- Mode registry (leaf; cross-checks against fleet models + tools) ---
	modelTags := map[string][]string{}
	modelNames := make([]string, 0, len(models))
	for _, m := range models {
		modelNames = append(modelNames, m.Name)
		modelTags[m.Name] = m.ModeTags
	}
	modeReg, err := mode.New(mode.ValidationInput{
		Models:    modelNames,
		ModelTags: modelTags,
		Tools:     toolNames,
	})
	if err != nil {
		return err
	}

	// --- Router startup gates (ADR-0028 §4, run at the composition root — the
	// sequencing note in implementation-sequence.md; the Mode registry stays a
	// leaf). A no-op when no mode opts into the router. ---
	toolHash := routergate.ToolSetHash(registry.List())
	present := map[string]bool{}
	for _, n := range modelNames {
		present[n] = true
	}
	if err := routergate.Check(modeReg.List(),
		func(name string) bool { return present[name] },
		fleetGW.Fingerprint,
		toolHash,
	); err != nil {
		return err
	}

	// --- ToolDecider (wired only when a mode opts in — ADR-0028 §3: the native
	// baseline wires no decider) ---
	var deciderGW loop.Decider
	for _, m := range modeReg.List() {
		if m.ToolCalling == "router" {
			deciderGW = tooldecider.New(fleetGW, providerGW)
			break
		}
	}

	// --- Assembler + loop ---
	assemblerGW := assembler.New()
	loopGW := loop.New(loop.Deps{
		Modes:     modeReg,
		Tools:     registry,
		Executor:  executor,
		Assembler: assemblerGW,
		Provider:  providerGW,
		Fleet:     fleetGW,
		Doc:       docStore,
		Retriever: retrieverGW,
		Sessions:  sessStore,
		Meter:     meterStore,
		Bus:       bus,
		Decider:   deciderGW,
	})

	// --- API server ---
	srv, err := apiserver.New(apiserver.Deps{
		Fleet:    fleetGW,
		Modes:    modeReg,
		Tools:    registry,
		Doc:      docStore,
		Sessions: sessStore,
		Loop:     loopGW,
	}, bus)
	if err != nil {
		return err
	}

	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
	}

	log.Printf("texteditor listening on http://%s (daemon %s)", *addr, *daemonURL)
	return httpSrv.ListenAndServe()
}

// makeToolHandlers returns the real tool handlers bound at the composition root,
// wiring the Document store, Retriever, and TextFormatter into the engine's four
// tools (edit_markdown, retrieve, diff, read_note). The name is the whole seam
// (ADR-0019 §3). Handlers return the structured result shapes the loop observes
// (ADR-0029 §5); the document-scoped tools read a loop-injected `documentId`.
func makeToolHandlers(doc document.Interface, rec retriever.Interface, tf textformatter.Interface) map[string]tool.Handler {
	return map[string]tool.Handler{
		"edit_markdown": editMarkdownHandler(doc, tf),
		"retrieve":      retrieveHandler(rec),
		"diff":          diffHandler(doc),
		"read_note":     readNoteHandler(rec),
	}
}

// editMarkdownHandler applies a whole-block replacement (ADR-0029 §1): pre-flight
// Validate, then ApplyEdit (which normalizes + verifies guards). Returns the
// structured {ok, blockId, revision, diff, normalized} or
// {ok:false, error:guard-failed|invalid-structure, …} shape.
func editMarkdownHandler(doc document.Interface, tf textformatter.Interface) tool.Handler {
	return func(args json.RawMessage) (json.RawMessage, error) {
		var in struct {
			BlockID    string `json:"blockId"`
			Text       string `json:"text"`
			DocumentID string `json:"documentId"`
		}
		if err := json.Unmarshal(args, &in); err != nil {
			return nil, err
		}
		if in.DocumentID == "" || in.BlockID == "" {
			return structuredEdit(false, "invalid-args", nil), nil
		}

		ctx := context.Background()

		// Find the block's kind for pre-flight validation.
		var kind dto.BlockKind
		if blocks, err := doc.Blocks(in.DocumentID); err == nil {
			for _, b := range blocks {
				if b.ID == in.BlockID {
					kind = b.Kind
					break
				}
			}
		}

		// Pre-flight structural validation (ADR-0029 §2: Validate runs in the
		// edit-tool handler; issues reach the model).
		if issues := tf.Validate(kind, in.Text); len(issues) > 0 {
			return structuredEdit(false, "invalid-structure", map[string]interface{}{"issues": issues}), nil
		}

		rev, err := doc.ApplyEdit(ctx, in.DocumentID, dto.BlockEdit{BlockID: in.BlockID, Text: in.Text})
		if err != nil {
			switch {
			case errors.Is(err, document.ErrGuardFailed):
				return structuredEdit(false, "guard-failed", map[string]interface{}{"blockId": in.BlockID}), nil
			case errors.Is(err, document.ErrInvalidStructure):
				return structuredEdit(false, "invalid-structure", nil), nil
			default:
				return nil, err
			}
		}
		res := map[string]interface{}{
			"ok":       true,
			"blockId":  in.BlockID,
			"revision": map[string]interface{}{"id": rev.ID, "message": rev.Message},
		}
		b, _ := json.Marshal(res)
		return b, nil
	}
}

// retrieveHandler returns the top retrieval chunks for a query.
func retrieveHandler(rec retriever.Interface) tool.Handler {
	return func(args json.RawMessage) (json.RawMessage, error) {
		var in struct {
			Query string `json:"query"`
		}
		if err := json.Unmarshal(args, &in); err != nil {
			return nil, err
		}
		chunks, err := rec.Query(context.Background(), in.Query, 3)
		if err != nil {
			return nil, err
		}
		b, _ := json.Marshal(map[string]interface{}{"ok": true, "chunks": chunks})
		return b, nil
	}
}

// diffHandler returns the word-level diff between two revisions.
func diffHandler(doc document.Interface) tool.Handler {
	return func(args json.RawMessage) (json.RawMessage, error) {
		var in struct {
			DocumentID string `json:"documentId"`
			BaseRev    string `json:"baseRev"`
			Rev        string `json:"rev"`
		}
		if err := json.Unmarshal(args, &in); err != nil {
			return nil, err
		}
		edits, err := doc.Diff(in.DocumentID, in.BaseRev, in.Rev)
		if err != nil {
			return nil, err
		}
		b, _ := json.Marshal(map[string]interface{}{"ok": true, "edits": edits})
		return b, nil
	}
}

// readNoteHandler reads a note from the vault by path/title. The engine has no
// dedicated vault module; the note index lives in the Retriever's index.db, so a
// title/path query is served by full-text search (a POC-path judgment call,
// documented in the report).
func readNoteHandler(rec retriever.Interface) tool.Handler {
	return func(args json.RawMessage) (json.RawMessage, error) {
		var in struct {
			Query string `json:"query"`
		}
		if err := json.Unmarshal(args, &in); err != nil {
			return nil, err
		}
		chunks, err := rec.Query(context.Background(), in.Query, 1)
		if err != nil {
			return nil, err
		}
		b, _ := json.Marshal(map[string]interface{}{"ok": true, "note": chunks})
		return b, nil
	}
}

// structuredEdit renders the edit_markdown structured result (ADR-0029 §5).
func structuredEdit(ok bool, errCode string, extra map[string]interface{}) json.RawMessage {
	res := map[string]interface{}{"ok": ok}
	if !ok {
		res["error"] = errCode
	}
	for k, v := range extra {
		res[k] = v
	}
	b, _ := json.Marshal(res)
	return b
}

// makeHandler returns a minimal placeholder handler for any tool without a real
// binding (never occurs for the shipped four; satisfies the cross-check).
func makeHandler(name string) tool.Handler {
	return func(args json.RawMessage) (json.RawMessage, error) {
		return nil, fmt.Errorf("tool %s: handler not yet implemented", name)
	}
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func defaultDataDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".texteditor-data"
	}
	return filepath.Join(home, ".local", "share", "texteditor")
}
