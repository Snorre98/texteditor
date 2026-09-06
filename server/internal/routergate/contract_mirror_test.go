package routergate

// Contract-mirror drift check (ADR-0033 §3): the needle facade contract is
// canonical in macos-dev-config (the serving side, where the facade lives);
// texteditor keeps a mirror at
//
//	docs/writing-assistant/contracts/needle-facade.md
//
// This test fails when the sibling macos-dev-config checkout is present and the
// mirror drifts from canonical. It skips silently when the sibling repo is not
// checked out (e.g. fresh clones of texteditor alone) — the same pattern as the
// fleet contract mirror test (internal/fleet/contract_mirror_test.go), which
// owns the repo-root/canonical-root walk helpers this file reuses.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const needleFacadeMirrorBanner = "<!-- MIRROR — canonical: macos-dev-config/docs/contracts/needle-facade.md"

// TestNeedleFacadeContractMirror checks that the texteditor mirror of the needle
// facade contract is byte-identical to the canonical copy in macos-dev-config
// (modulo the mirror banner). Drift means a second needle variant would conform
// to a different contract — the failure class ADR-0033 §3's sync check exists to
// prevent.
func TestNeedleFacadeContractMirror(t *testing.T) {
	mirror, ok := readMirror(t)
	if !ok {
		return // sibling absent; skip silently
	}

	canonical := readOrFatalFacade(t, filepath.Join(canonicalRootFacade(t), "docs", "contracts", "needle-facade.md"))
	if mirror != canonical {
		t.Fatal("needle-facade.md mirror drifted from macos-dev-config/docs/contracts/needle-facade.md — edit the canonical copy and re-sync")
	}
}

func readMirror(t *testing.T) (string, bool) {
	t.Helper()
	mirror := readOrFatalFacade(t, filepath.Join(repoRootFacade(t), "docs", "writing-assistant", "contracts", "needle-facade.md"))
	if !strings.HasPrefix(mirror, needleFacadeMirrorBanner) {
		t.Fatalf("needle-facade.md mirror is missing its MIRROR banner (ADR-0033 §3)")
	}
	return strings.TrimPrefix(mirror, strings.SplitN(mirror, "\n", 2)[0]+"\n"), true
}

// repoRootFacade walks up to the repo root (docs/writing-assistant); since
// ADR-0034 the Go module (go.mod) lives in server/.
func repoRootFacade(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
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

func canonicalRootFacade(t *testing.T) string {
	t.Helper()
	sibling := filepath.Join(filepath.Dir(repoRootFacade(t)), "macos-dev-config")
	if _, err := os.Stat(filepath.Join(sibling, "go.mod")); err != nil {
		t.Skip("macos-dev-config checkout not present; skipping needle-facade mirror drift check")
	}
	return sibling
}

func readOrFatalFacade(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}
