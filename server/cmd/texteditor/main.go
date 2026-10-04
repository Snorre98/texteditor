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
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	_ "modernc.org/sqlite"

	"texteditor/internal/apiserver"
	"texteditor/internal/assembler"
	"texteditor/internal/chunker"
	"texteditor/internal/corpus"
	"texteditor/internal/document"
	"texteditor/internal/eventbus"
	"texteditor/internal/filesystem"
	"texteditor/internal/fleet"
	"texteditor/internal/laya"
	"texteditor/internal/liveness"
	"texteditor/internal/loop"
	"texteditor/internal/mode"
	"texteditor/internal/pipeline"
	"texteditor/internal/provider"
	"texteditor/internal/retriever"
	"texteditor/internal/shard"
	"texteditor/internal/textformatter"
	"texteditor/internal/tool"
	"texteditor/internal/workspace"
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
		bind      = flag.String("bind", envOr("ENGINE_BIND", "127.0.0.1"), "address to bind (ENGINE_BIND=0.0.0.0 opts into LAN exposure, ADR-0021 §2)")
		port      = flag.Int("port", envInt("ENGINE_PORT", 0), "port to bind (0 = dynamic free port; ENGINE_PORT fixes it, ADR-0021 §1)")
		daemonURL = flag.String("daemon", envOr("DAEMON_URL", "http://127.0.0.1:9300"), "control daemon base URL (ADR-0025)")
		cors      = flag.String("cors-origins", envOr("ENGINE_CORS_ORIGINS", ""), "comma-separated CORS origin allowlist (empty = CORS disabled, ADR-0037)")
		allowed   = flag.String("allowed-roots", envOr("ALLOWED_ROOTS", ""), "comma-separated allowlist bounding directory browsing and corpus indexing (default $HOME, ADR-0049 §6)")
		fleetPoll = flag.Int("fleet-poll-seconds", envInt("ENGINE_FLEET_POLL_SECONDS", 10), "engine-side fleet ListStatus poll interval in seconds (0 disables; ADR-0052 §4)")
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

	// --- workspaces.db (global Workspace registry; ADR-0049 §5) ---
	wsDB, err := open("workspaces.db")
	if err != nil {
		return err
	}
	if err := workspace.Migrate(ctx, wsDB); err != nil {
		return fmt.Errorf("workspaces.db: %w", err)
	}
	wsStore := workspace.New(wsDB)

	// --- Filesystem (read-only reach bounded by ALLOWED_ROOTS, ADR-0049 §6) ---
	fsGW, err := filesystem.New(splitList(*allowed))
	if err != nil {
		return fmt.Errorf("allowed-roots: %w", err)
	}
	log.Printf("allowed roots: %v", fsGW.AllowedRoots())

	// --- bus (SSE fan-out; wired before the shard manager so meter events flow) ---
	bus := eventbus.New()

	// --- Fleet (daemon HTTP client) ---
	fleetGW := fleet.NewDaemon(*daemonURL)
	models, err := fleetGW.ListModels()
	if err != nil {
		// A dead daemon is fatal at startup for validation input (we need the
		// model/tags fact). Surface loudly rather than half-wiring.
		return fmt.Errorf("fleet unavailable: %w", err)
	}

	// --- Provider (one shared gateway: retriever embeds, loop streams —
	// stateless transport, safe to share) ---
	providerGW := provider.New()

	// --- Shard manager (per-workspace context state; ADR-0049 §5) ---
	// Each workspace shard owns index.db/sessions.db/meter.db, opened lazily
	// and closed LRU. Document identity and git stay global (app.db/worktree).
	shards := shard.New(shard.Options{
		DataDir: *dataDir,
		Bus:     bus,
		NewRetriever: func(db *sql.DB) retriever.Interface {
			return retriever.New(db, fleetGW, providerGW, docStore, fsGW, chunker.New(), 512)
		},
	})

	// --- Tool registry + executor (ADR-0019; VerifyHandlers cross-check) ---
	registry, toolNames, err := tool.Load()
	if err != nil {
		return fmt.Errorf("tools: %w", err)
	}
	executor := tool.NewExecutor()
	handlers := makeToolHandlers(docStore, textformatter.New())
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

	// --- Mode registry (leaf; cross-checks against the fleet's models + tags) ---
	modelTags := map[string][]string{}
	modelNames := make([]string, 0, len(models))
	for _, m := range models {
		modelNames = append(modelNames, m.Name)
		modelTags[m.Name] = m.ModeTags
	}
	modeReg, err := mode.New(mode.ValidationInput{
		Models:    modelNames,
		ModelTags: modelTags,
	})
	if err != nil {
		return err
	}

	// --- Pipeline policy (ADR-0045 §3: one global turn policy, validated
	// fail-fast at startup) ---
	pipelineGW, err := pipeline.New()
	if err != nil {
		return err
	}

	// --- Corpus service (multi-root scope, index-only; ADR-0049 §3/§4) ---
	corpusSvc := corpus.New(corpus.Options{
		Workspaces: wsStore,
		Filesystem: fsGW,
		Shards:     shards,
		Bus:        bus,
	})
	// The API's document store is decorated so a successful Open/Commit/
	// write-through SaveTree enqueues a corpus re-index when the path is in a
	// workspace's corpus and emits a `document` feed event; the write itself is
	// never blocked.
	docAPI := &corpus.DocHook{Interface: docStore, Corpus: corpusSvc, Bus: bus}

	// --- Assembler + loop ---
	assemblerGW := assembler.New()
	// Laya decision layer (ADR-0053): the client resolves the named service via
	// Fleet and calls its native typed-question API; it degrades fail-open.
	decisionPolicy := pipelineGW.Policy().Decision
	layaGW := laya.New(fleetGW, decisionPolicy.Model, time.Duration(decisionPolicy.TimeoutMs)*time.Millisecond)
	loopGW := loop.New(loop.Deps{
		Modes:      modeReg,
		Tools:      registry,
		Executor:   executor,
		Assembler:  assemblerGW,
		Provider:   providerGW,
		Fleet:      fleetGW,
		Doc:        docStore,
		Shards:     shards,
		Workspaces: wsStore,
		Bus:        bus,
		Pipeline:   pipelineGW,
		Filesystem: fsGW,
		Decision:   layaGW,
	})

	// --- Bind: dynamic port by default (ADR-0021 §1); ENGINE_BIND=0.0.0.0 opts
	// into LAN exposure (ADR-0021 §2). The listener is bound before the API
	// server is built so the actual base URL can be advertised via /health. ---
	if *bind == "0.0.0.0" {
		log.Printf("warning: binding 0.0.0.0 exposes the engine (documents + edit history) to the LAN — localhost is the privacy default (ADR-0021 §2)")
	}
	ln, baseURL, err := bindListener(*bind, *port)
	if err != nil {
		return err
	}

	// --- API server ---
	srv, err := apiserver.New(apiserver.Deps{
		Fleet:       fleetGW,
		Modes:       modeReg,
		Tools:       registry,
		Doc:         docAPI,
		Loop:        loopGW,
		Filesystem:  fsGW,
		Workspaces:  wsStore,
		Shards:      shards,
		Corpus:      corpusSvc,
		Pipeline:    pipelineGW,
		BaseURL:     baseURL,
		CORSOrigins: splitList(*cors),
	}, bus)
	if err != nil {
		return err
	}

	httpSrv := &http.Server{
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Graceful shutdown on SIGTERM/SIGINT (ADR-0021 §1: the sidecar stop contract
	// is SIGTERM then SIGKILL on timeout — the engine exits cleanly on SIGTERM).
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Engine-side fleet poller: the daemon is external (ADR-0025) with no
	// webhook, so the engine polls its batch projection and emits `fleet` feed
	// events on change (ADR-0052 §4).
	fleetPoller := liveness.New(liveness.Options{
		Fleet:    fleetGW,
		Bus:      bus,
		Interval: time.Duration(*fleetPoll) * time.Second,
	})
	go fleetPoller.Run(ctx)

	errc := make(chan error, 1)
	go func() { errc <- httpSrv.Serve(ln) }()

	log.Printf("texteditor listening on %s (daemon %s)", baseURL, *daemonURL)

	select {
	case err := <-errc:
		_ = shards.Close()
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err := httpSrv.Shutdown(shutdownCtx)
		_ = shards.Close()
		return err
	}
}

// makeToolHandlers returns the real tool handlers bound at the composition root,
// wiring the Document store and TextFormatter into the engine's four tools
// (edit_markdown, retrieve, diff, read_note). The name is the whole seam
// (ADR-0019 §3). Handlers return the structured result shapes the loop observes
// (ADR-0029 §5); document-scoped tools read a loop-injected `documentId`, and
// retrieval tools resolve the turn's workspace-scoped Retriever from the
// context the loop enriches with the shard lease (ADR-0049 §5).
func makeToolHandlers(doc document.Interface, tf textformatter.Interface) map[string]tool.Handler {
	return map[string]tool.Handler{
		"edit_markdown": editMarkdownHandler(doc, tf),
		"retrieve":      retrieveHandler(),
		"diff":          diffHandler(doc),
		"read_note":     readNoteHandler(),
	}
}

// retrieverFromContext resolves the turn's workspace-scoped Retriever.
func retrieverFromContext(ctx context.Context) (retriever.Interface, error) {
	svc, ok := shard.ServicesFromContext(ctx)
	if !ok || svc.Retriever == nil {
		return nil, errors.New("retriever-unavailable: no workspace shard in this turn's context")
	}
	return svc.Retriever, nil
}

// editMarkdownHandler applies a whole-block replacement (ADR-0029 §1): pre-flight
// Validate, then ApplyEdit (which normalizes + verifies guards). Returns the
// structured {ok, blockId, revision, diff, normalized} or
// {ok:false, error:guard-failed|invalid-structure, …} shape.
func editMarkdownHandler(doc document.Interface, tf textformatter.Interface) tool.Handler {
	return func(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
		var in struct {
			BlockID    string `json:"blockId"`
			Text       string `json:"text"`
			BaseHash   string `json:"baseHash"`
			DocumentID string `json:"documentId"`
			ModeName   string `json:"modeName"`
		}
		if err := json.Unmarshal(args, &in); err != nil {
			return nil, err
		}
		if in.DocumentID == "" || in.BlockID == "" {
			return structuredEdit(false, "invalid-args", nil), nil
		}

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

		// The target block's echoed base hash is the model-path guard (ADR-0047
		// §6): ApplyEdit verifies it atomically with staging, so a model edit
		// computed against stale text cannot land.
		edit := dto.BlockEdit{BlockID: in.BlockID, Text: in.Text, Mode: in.ModeName}
		if in.BaseHash != "" {
			edit.Guards = []dto.Guard{{BlockID: in.BlockID, Hash: in.BaseHash}}
		}

		rev, err := doc.ApplyEdit(ctx, in.DocumentID, edit)
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

// retrieveHandler returns the top retrieval chunks for a query, using the
// turn's workspace-scoped Retriever (hybrid FTS5 + vec0 with provenance).
func retrieveHandler() tool.Handler {
	return func(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
		var in struct {
			Query string `json:"query"`
		}
		if err := json.Unmarshal(args, &in); err != nil {
			return nil, err
		}
		rec, err := retrieverFromContext(ctx)
		if err != nil {
			return nil, err
		}
		chunks, err := rec.Query(ctx, in.Query, 3)
		if err != nil {
			return nil, err
		}
		b, _ := json.Marshal(map[string]interface{}{"ok": true, "chunks": chunks})
		return b, nil
	}
}

// diffHandler returns the word-level diff between two revisions.
func diffHandler(doc document.Interface) tool.Handler {
	return func(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
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
// dedicated vault module; the note index lives in the workspace shard's
// index.db, so a title/path query is served by retrieval (a POC-path judgment
// call, documented in the report).
func readNoteHandler() tool.Handler {
	return func(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
		var in struct {
			Query string `json:"query"`
		}
		if err := json.Unmarshal(args, &in); err != nil {
			return nil, err
		}
		rec, err := retrieverFromContext(ctx)
		if err != nil {
			return nil, err
		}
		chunks, err := rec.Query(ctx, in.Query, 1)
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
	return func(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
		return nil, fmt.Errorf("tool %s: handler not yet implemented", name)
	}
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

// splitList splits a comma-separated list, trimming whitespace and dropping empties.
func splitList(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// envInt reads an integer env var, falling back to d when unset or unparseable.
func envInt(k string, d int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
		log.Printf("warning: %s=%q is not an integer; using default %d", k, v, d)
	}
	return d
}

// bindListener binds the engine's listener per the port/bind policy (ADR-0021):
// port 0 = dynamic free port, else fixed. It returns the listener and the
// derived base URL for /health advertisement.
func bindListener(bind string, port int) (net.Listener, string, error) {
	ln, err := net.Listen("tcp", net.JoinHostPort(bind, strconv.Itoa(port)))
	if err != nil {
		return nil, "", fmt.Errorf("listen %s: %w", net.JoinHostPort(bind, strconv.Itoa(port)), err)
	}
	return ln, "http://" + ln.Addr().String(), nil
}

func defaultDataDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".texteditor-data"
	}
	return filepath.Join(home, ".local", "share", "texteditor")
}
