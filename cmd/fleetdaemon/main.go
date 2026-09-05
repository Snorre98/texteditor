// Command fleetdaemon runs the serving control daemon: the HTTP transport over
// the lifecycle verb contract (ADR-0025, ADR-0027), the sole reader of
// models.json (ADR-0027), and the wrapper of serve.sh (ADR-0025 §1). It is the
// server mirror of the engine's internal/fleet client, authored and CI-tested
// in one module (ADR-0032). It binds 127.0.0.1 by default and enforces the
// pre-bind gate (ADR-0021 §3). Single static Go binary, no CGO (ADR-0003).
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"texteditor/internal/fleetdaemon"
)

// version is the built-from tag, set at build time via -ldflags
// "-X main.version=<tag>" and reported by --version so "am I current" is
// scriptable on any machine (ADR-0032 §2).
var version = "devel"

func main() {
	if err := run(); err != nil {
		log.Fatalf("fleetdaemon: %v", err)
	}
}

func run() error {
	var (
		manifest = flag.String("manifest", defaultManifest(), "path to models.json (the daemon is its sole reader)")
		addr     = flag.String("addr", envOr("DAEMON_ADDR", "127.0.0.1:9300"), "HTTP listen address (default 127.0.0.1)")
		varDir   = flag.String("var", envOr("DAEMON_VAR", defaultVarDir()), "directory for runner logs + provision scratch")
		serveSh  = flag.String("serve-sh", envOr("SERVE_SH", defaultServeSh()), "absolute path to serve.sh (the daemon wraps it, ADR-0025 §1)")
		showVer  = flag.Bool("version", false, "print the built-from version tag and exit")
	)
	flag.Parse()

	if *showVer {
		fmt.Println(version)
		return nil
	}
	if len(flag.Args()) > 0 && flag.Arg(0) == "version" {
		fmt.Println(version)
		return nil
	}

	if err := os.MkdirAll(*varDir, 0o755); err != nil {
		return err
	}

	m, err := fleetdaemon.Load(*manifest)
	if err != nil {
		return fmt.Errorf("manifest: %w", err)
	}

	daemon := fleetdaemon.New(m, *varDir, *serveSh)

	srv := &http.Server{
		Addr:              *addr,
		Handler:           daemon.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	log.Printf("fleetdaemon %s listening on http://%s (manifest %s)", version, *addr, *manifest)
	return srv.ListenAndServe()
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func defaultManifest() string {
	if v := os.Getenv("MODELS_JSON"); v != "" {
		return v
	}
	// Same default as macos-dev-config: <repo>/models.json. When running from a
	// drop-shipped bin/ inside that repo, the manifest sits two levels up.
	exe, err := os.Executable()
	if err == nil {
		root := filepath.Dir(filepath.Dir(exe)) // bin/fleetdaemon -> repo root
		p := filepath.Join(root, "models.json")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return "models.json"
}

func defaultServeSh() string {
	exe, err := os.Executable()
	if err != nil {
		return "serve.sh"
	}
	return filepath.Join(filepath.Dir(filepath.Dir(exe)), "tools", "serve.sh")
}

func defaultVarDir() string {
	exe, err := os.Executable()
	if err != nil {
		return "var"
	}
	return filepath.Join(filepath.Dir(filepath.Dir(exe)), "var")
}
