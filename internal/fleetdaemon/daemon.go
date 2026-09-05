package fleetdaemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"texteditor/shared/dto"
)

// Typed lifecycle errors surfaced over the daemon HTTP contract as
// {code,message} (interface.md §12.1, daemon-http.md §4).
var (
	ErrUnknownServer = errors.New("unknown-server")
	ErrPortInUse     = errors.New("port-in-use")
	ErrModelNotFound = errors.New("model-not-found")
	ErrBinaryMissing = errors.New("binary-missing")
	ErrNotRunning    = errors.New("not-running")
	ErrStartTimeout  = errors.New("start-timeout")
)

// startBound is the blocking-start health wait (daemon-http.md §2 `start`).
const startBound = 60 * time.Second

// Control is the control daemon: manifest + live state + lifecycle orchestration.
// It owns manifest parse, lanes, provision, and live state (ADR-0032 §4); it
// wraps serve.sh rather than re-implementing runner launch (ADR-0025 §1).
type Control struct {
	manifest *Manifest
	states   *stateRegistry
	runner   runnerCommand

	// varDir is where runner logs and provision scratch live (macos-dev-config's
	// var/ convention, ADR-0032 §6).
	varDir string

	mu          sync.Mutex
	procs       map[string]*exec.Cmd // name -> running/finished runner process
	provision   map[string]*provisionJob
	startCh     map[string]chan error // in-flight blocking start awaits
	serveShPath string
}

// New builds a Control over a parsed manifest.
func New(m *Manifest, varDir, serveShPath string) *Control {
	names := make([]string, 0, len(m.Models))
	for _, mo := range m.Models {
		names = append(names, mo.Name)
	}
	return &Control{
		manifest:    m,
		states:      newStateRegistry(names),
		runner:      commandRunner(),
		varDir:      varDir,
		serveShPath: serveShPath,
		procs:       map[string]*exec.Cmd{},
		provision:   map[string]*provisionJob{},
		startCh:     map[string]chan error{},
	}
}

// SetRunner overrides the runner seam (tests).
func (d *Control) SetRunner(r runnerCommand) { d.runner = r }

// Manifest returns the parsed manifest (read-only).
func (d *Control) Manifest() *Manifest { return d.manifest }

// --- list ---

// List returns the daemon's full model projection (daemon-http.md §2 `list`).
func (d *Control) List() []daemonEntry {
	entries := d.manifest.listEntries()
	// Attach live state to each entry (state is daemon-owned, ADR-0025).
	out := make([]daemonEntry, 0, len(entries))
	for _, e := range entries {
		if st, ok := d.states.get(e.Name); ok {
			e.state = st
		}
		out = append(out, e)
	}
	return out
}

// entryByName returns a list entry by model name (unknown-server when absent).
func (d *Control) entryByName(name string) (daemonEntry, error) {
	for _, e := range d.List() {
		if e.Name == name {
			return e, nil
		}
	}
	return daemonEntry{}, ErrUnknownServer
}

// --- status ---

// Status returns a model's live state plus optional provisioning progress.
func (d *Control) Status(name string) (dto.LiveState, error) {
	if _, _, ok := d.manifest.modelByName(name); !ok {
		return dto.LiveUnknown, ErrUnknownServer
	}
	st, _ := d.states.get(name)
	return st, nil
}

// StatusWithProgress returns state + (bytes,total) when provisioning.
func (d *Control) StatusWithProgress(name string) (dto.LiveState, int64, int64, error) {
	st, err := d.Status(name)
	if err != nil {
		return st, 0, 0, err
	}
	if st == dto.LiveProvisioning {
		b, t, _ := d.states.progress(name)
		return st, b, t, nil
	}
	return st, 0, 0, nil
}

// --- pre-bind gate + spawn ---

// preBindGate refuses a non-localhost bind that is not ACL-gated (ADR-0021 §3,
// ADR-0032 §5). It runs before any serve.sh spawn, fail-closed.
func (d *Control) preBindGate(host string) error {
	if host == "" || host == "127.0.0.1" || host == "localhost" || host == "::1" {
		return nil
	}
	return fmt.Errorf("refusing bind %q: non-localhost bind requires a Tailscale ACL gate (ADR-0021 §3)", host)
}

// Start launches one model server, blocking until healthy (up to startBound).
// It enforces idempotency, port-in-use, binary-missing, model-not-found, and the
// pre-bind gate (daemon-http.md §2 `start`).
func (d *Control) Start(ctx context.Context, name string) (dto.LiveState, error) {
	mo, dmn, ok := d.manifest.modelByName(name)
	if !ok {
		return dto.LiveUnknown, ErrUnknownServer
	}
	_ = mo

	if err := d.preBindGate(dmn.Host); err != nil {
		return dto.LiveUnknown, err
	}

	// Idempotent: already-up is only an error when status reports up.
	if st, _ := d.states.get(name); st == dto.LiveUp {
		return st, nil
	}

	// Serialize one start per model (daemon-request queue, concurrency-topology §2).
	d.mu.Lock()
	ch, inFlight := d.startCh[name]
	if inFlight {
		d.mu.Unlock()
		select {
		case <-ctx.Done():
			return dto.LiveUnknown, ctx.Err()
		case <-ch:
			st, _ := d.states.get(name)
			return st, nil
		}
	}
	ch = make(chan error, 1)
	d.startCh[name] = ch
	d.mu.Unlock()

	defer func() {
		d.mu.Lock()
		delete(d.startCh, name)
		d.mu.Unlock()
	}()

	st, err := d.startLocked(ctx, name, dmn)
	ch <- err
	return st, err
}

func (d *Control) startLocked(ctx context.Context, name string, dmn Daemon) (dto.LiveState, error) {
	// Port-in-use: detect before launch.
	if err := d.portInUse(dmn.Host, dmn.Port, name); err != nil {
		return dto.LiveDown, err
	}

	host := dmn.Host
	if host == "" {
		host = "127.0.0.1"
	}

	model, _, _ := d.manifest.modelByName(name)
	spec := runnerSpec{
		Runner:   dmn.Runner,
		Model:    modelField(model, dmn),
		Delegate: dmn.Delegate,
		Host:     host,
		Port:     dmn.Port,
		ServeSh:  d.serveShPath,
	}
	if dmn.Runner == "delegate" {
		spec.Model = ""
	}

	d.states.set(name, dto.LiveStarting)
	cmd, err := d.runner(spec)
	if err != nil {
		d.states.set(name, dto.LiveDown)
		return dto.LiveDown, err
	}

	if err := cmd.Start(); err != nil {
		if isBinaryMissing(err) {
			d.states.set(name, dto.LiveDown)
			return dto.LiveDown, ErrBinaryMissing
		}
		d.states.set(name, dto.LiveDown)
		return dto.LiveDown, err
	}
	d.mu.Lock()
	d.procs[name] = cmd
	d.mu.Unlock()

	// Wait for health within startBound.
	deadline := time.NewTimer(startBound)
	defer deadline.Stop()
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return dto.LiveStarting, ctx.Err()
		case <-deadline.C:
			return dto.LiveStarting, ErrStartTimeout
		case <-tick.C:
			if d.healthy(host, dmn.Port) {
				d.states.set(name, dto.LiveUp)
				return dto.LiveUp, nil
			}
		}
	}
}

// modelField resolves the value for the MODEL env var handed to serve.sh:
// llama.cpp takes a GGUF file (source.file); mlx-* take the HF repo id.
func modelField(mo Model, dmn Daemon) string {
	switch mo.Source.Kind {
	case "gguf":
		return mo.Source.File
	case "hf":
		return mo.Source.Repo
	default:
		return ""
	}
}

// portInUse checks whether host:port already has a listener; a closed port or
// refusal-to-connect is treated as free (curl-style health check semantics).
func (d *Control) portInUse(host string, port int, name string) error {
	addr := net.JoinHostPort(host, itoa(port))
	conn, err := net.DialTimeout("tcp", addr, 300*time.Millisecond)
	if err == nil {
		conn.Close()
		return fmt.Errorf("%w: port %d already bound (remap with SERVE_PORT_%s=<newport>)", ErrPortInUse, port, envName(name))
	}
	return nil
}

// healthy polls a runner's endpoints (ADR-0007: /health, /v1/models, /api/tags).
func (d *Control) healthy(host string, port int) bool {
	base := fmt.Sprintf("http://%s:%d", host, port)
	for _, p := range []string{"/health", "/v1/models", "/api/tags"} {
		c := &http.Client{Timeout: 2 * time.Second} // short poll
		if resp, err := c.Get(base + p); err == nil {
			resp.Body.Close()
			if resp.StatusCode < 500 {
				return true
			}
		}
	}
	return false
}

// envName renders the SERVE_PORT_<NAME> remap-hint key from a model name
// (serve.sh's upper-cased + underscored convention).
func envName(name string) string {
	return regexp.MustCompile(`[^a-zA-Z0-9]`).ReplaceAllString(strings.ToUpper(name), "_")
}

// Stop terminates a model server (idempotent no-op when down, daemon-http.md §2).
func (d *Control) Stop(ctx context.Context, name string) (dto.LiveState, error) {
	if _, _, ok := d.manifest.modelByName(name); !ok {
		return dto.LiveUnknown, ErrUnknownServer
	}
	st, _ := d.states.get(name)
	if st == dto.LiveDown || st == dto.LiveUnknown || st == dto.LiveStopping {
		return st, nil // idempotent no-op (stop on down is a no-op warning)
	}

	d.states.set(name, dto.LiveStopping)
	d.mu.Lock()
	cmd := d.procs[name]
	d.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	d.mu.Lock()
	delete(d.procs, name)
	d.mu.Unlock()
	d.states.set(name, dto.LiveDown)
	return dto.LiveDown, nil
}

// Log tails a server's log (read-only). The daemon's own log is separate from
// runner logs (ADR-0032 §6); runner logs live under var/serve-<name>.log.
func (d *Control) Log(name string) ([]string, error) {
	if _, _, ok := d.manifest.modelByName(name); !ok {
		return nil, ErrUnknownServer
	}
	path := fmt.Sprintf("%s/serve-%s.log", d.varDir, name)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, ErrNotRunning
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		lines = nil
	}
	const tail = 200
	if len(lines) > tail {
		lines = lines[len(lines)-tail:]
	}
	return lines, nil
}

// Reach returns the base URL + a curl example for one model (daemon-http.md §2).
func (d *Control) Reach(name string) (baseURL, curl string, err error) {
	_, dmn, ok := d.manifest.modelByName(name)
	if !ok {
		return "", "", ErrUnknownServer
	}
	host := dmn.Host
	if host == "" {
		host = "127.0.0.1"
	}
	baseURL = fmt.Sprintf("http://%s:%d/v1", host, dmn.Port)
	curl = fmt.Sprintf("curl %s/models", baseURL)
	return baseURL, curl, nil
}

// isBinaryMissing reports whether a spawn error means the runner binary is not on
// PATH (exec.ErrNotFound).
func isBinaryMissing(err error) bool {
	return errors.Is(err, exec.ErrNotFound)
}
