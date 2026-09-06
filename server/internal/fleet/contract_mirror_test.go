package fleet

// Contract-mirror drift checks (ADR-0033 §3): the canonical daemon HTTP contract
// and the fleet-manifest JSON Schema live in macos-dev-config (the machine-local
// LLM control plane). texteditor keeps mirror copies:
//
//   - docs/writing-assistant/contracts/daemon-http.md          (mirror)
//   - docs/writing-assistant/contracts/assets/fleet-manifest.schema.json
//
// These tests fail when the sibling macos-dev-config checkout is present and the
// mirrors drift from canonical. They skip silently when the sibling repo is not
// checked out (e.g. fresh clones of texteditor alone).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const mirrorBanner = "<!-- MIRROR — canonical: macos-dev-config/docs/contracts/daemon-http.md"

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	// Walk up to the repo root (the dir holding docs/writing-assistant), not the
	// Go module root: since ADR-0034 the module (go.mod) lives in server/.
	for {
		if _, err := os.Stat(filepath.Join(dir, "docs", "writing-assistant")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("repo root (docs/writing-assistant) not found walking up from", dir)
		}
		dir = parent
	}
}

func canonicalRoot(t *testing.T) string {
	t.Helper()
	sibling := filepath.Join(filepath.Dir(repoRoot(t)), "macos-dev-config")
	if _, err := os.Stat(filepath.Join(sibling, "go.mod")); err != nil {
		t.Skip("macos-dev-config checkout not present; skipping contract-mirror drift check")
	}
	return sibling
}

func readOrFatal(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func TestDaemonHTTPContractMirror(t *testing.T) {
	mirror := readOrFatal(t, filepath.Join(repoRoot(t), "docs", "writing-assistant", "contracts", "daemon-http.md"))
	if !strings.HasPrefix(mirror, mirrorBanner) {
		t.Fatalf("daemon-http.md mirror is missing its MIRROR banner (ADR-0033 §3)")
	}
	body := strings.TrimPrefix(mirror, strings.SplitN(mirror, "\n", 2)[0]+"\n")
	canonical := readOrFatal(t, filepath.Join(canonicalRoot(t), "docs", "contracts", "daemon-http.md"))
	if body != canonical {
		t.Fatal("daemon-http.md mirror drifted from macos-dev-config/docs/contracts/daemon-http.md — edit the canonical copy and re-sync")
	}
}

func TestFleetManifestSchemaMirror(t *testing.T) {
	mirror := readOrFatal(t, filepath.Join(repoRoot(t), "docs", "writing-assistant", "contracts", "assets", "fleet-manifest.schema.json"))
	canonical := readOrFatal(t, filepath.Join(canonicalRoot(t), "internal", "fleetdaemon", "fleet-manifest.schema.json"))
	if mirror != canonical {
		t.Fatal("fleet-manifest.schema.json mirror drifted from macos-dev-config/internal/fleetdaemon/ — copy the canonical schema over the mirror")
	}
}
