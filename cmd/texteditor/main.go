// Command texteditor runs the writing-assistant engine: a single static Go
// binary (ADR-0003) exposing the REST/SSE surface (ADR-0017) and reaching
// serving only via the control daemon (ADR-0025/0027). No CGO.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
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
	"texteditor/internal/session"
	"texteditor/internal/textformatter"
	"texteditor/internal/tool"
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

	// --- Tool registry + executor (ADR-0019; VerifyHandlers cross-check) ---
	registry, toolNames, err := tool.Load()
	if err != nil {
		return fmt.Errorf("tools: %w", err)
	}
	executor := tool.NewExecutor()
	for _, name := range toolNames {
		executor.Bind(name, makeHandler(name))
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

	// --- Retriever (index.db; block source = Document store) ---
	indexDB, err := open("index.db")
	if err != nil {
		return err
	}
	retrieverGW := retriever.New(indexDB, fleetGW, provider.New(), docStore, chunker.New(), 512)

	// --- Assembler + loop ---
	assemblerGW := assembler.New()
	loopGW := loop.New(loop.Deps{
		Modes:     modeReg,
		Tools:     registry,
		Executor:  executor,
		Assembler: assemblerGW,
		Provider:  provider.New(),
		Fleet:     fleetGW,
		Doc:       docStore,
		Retriever: retrieverGW,
		Sessions:  sessStore,
		Meter:     meterStore,
		Bus:       bus,
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

// makeHandler returns a minimal tool handler. In the POC the modes are
// single-shot (non-agentic, maxSteps 0) so tools are not dispatched on the happy
// path; these handlers satisfy the startup cross-check and return a typed
// unavailable result if invoked. Real handlers are wired when the agentic loop
// and edit-integrity path land (ADR-0029).
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
