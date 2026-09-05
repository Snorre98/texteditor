// Package fleet holds the Fleet gateway — the engine's ONLY reach into serving
// (ADR-0016 §1, ADR-0025). It owns model discovery, resolution (merge + capability
// gates + fallback ladder), and lifecycle.
//
// The Fleet gateway is the control daemon's HTTP client (ADR-0025): it reads
// serving state only through the daemon's verb contract (interface.md §12,
// contracts/daemon-http.md) and never touches models.json or serve.sh directly
// (ADR-0025, ADR-0027). It also never learns runner/source/provisioning fields —
// those are daemon-owned (ADR-0016 §1); the only daemon-only field it consumes
// internally for Resolve's merge is each model's `defaults`.
package fleet

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"texteditor/shared/dto"
)

// FleetGateway is the Fleet gateway public API (interface.md §1).
type FleetGateway interface {
	ListModels() ([]dto.Model, error)
	Resolve(name string, opts dto.ResolveOpts) (dto.Resolution, error)
	Status(name string) (dto.LiveState, error)
	Start(name string) error
	Stop(name string) error
	Provision(ctx context.Context, name string) (string, error)
}

// Interface is an alias for FleetGateway (the contracted name, interface.md §1).
type Interface = FleetGateway

// Typed errors surfaced by the Fleet gateway (failure-semantics §2).
var (
	ErrModelNotFound     = errors.New("model-not-found: name not in the fleet")
	ErrNoModelAvailable  = errors.New("no-model-available: no servable model for the mode")
	ErrDaemonUnreachable = errors.New("daemon-unreachable: no serving control")
	ErrStartTimeout      = errors.New("start-timeout: server did not report up within the bound")
	ErrPortInUse         = errors.New("port-in-use: target port already bound")
	ErrBinaryMissing     = errors.New("binary-missing: runner binary not on PATH")
)

// daemonEntry is the daemon's per-model projection (contracts/daemon-http.md
// §2 `list`). It carries `defaults` — a daemon-owned field the engine consumes to
// merge params, but which is intentionally absent from the public dto.Model
// (ADR-0016 §1). Not exported: it is the daemon wire shape, not a boundary DTO.
type daemonEntry struct {
	Name         string           `json:"name"`
	Host         string           `json:"host"`
	Port         int              `json:"port"`
	Capabilities dto.Capabilities `json:"capabilities"`
	Defaults     struct {
		Temperature float64 `json:"temperature"`
		MaxTokens   int     `json:"maxTokens"`
	} `json:"defaults"`
	ModeTags []string `json:"modeTags"`

	state dto.LiveState // fetched per-call via the status verb
}

// baseURL renders the OpenAI-compatible endpoint for a model.
func (e daemonEntry) baseURL() string {
	return fmt.Sprintf("http://%s:%d/v1", e.Host, e.Port)
}

// toModel maps a daemon entry to the public DTO (dropping daemon-owned fields).
func (e daemonEntry) toModel() dto.Model {
	return dto.Model{
		Name:         e.Name,
		BaseURL:      e.baseURL(),
		Capabilities: e.Capabilities,
		ModeTags:     e.ModeTags,
	}
}

// daemon is the concrete Fleet gateway backing the daemon HTTP client.
type daemon struct {
	baseURL string // daemon's scheme://host:port
	client  *http.Client
}

// NewDaemon returns the Fleet gateway backed by the control daemon's HTTP
// contract (ADR-0025, contracts/daemon-http.md). baseURL is the daemon's
// scheme://host:port (e.g. "http://127.0.0.1:9300").
func NewDaemon(baseURL string) FleetGateway {
	return &daemon{
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  &http.Client{Timeout: 70 * time.Second}, // Start blocks up to 60s
	}
}

// NewDaemonWithClient returns a daemon-backed gateway over a caller-supplied
// client (tests).
func NewDaemonWithClient(baseURL string, c *http.Client) FleetGateway {
	return &daemon{baseURL: strings.TrimRight(baseURL, "/"), client: c}
}

// list fetches the daemon's full model projection via the `list` verb.
func (d *daemon) list() ([]daemonEntry, error) {
	var rsp struct {
		Models []daemonEntry `json:"models"`
	}
	if err := d.do(context.Background(), http.MethodGet, "/list", nil, &rsp); err != nil {
		return nil, err
	}
	return rsp.Models, nil
}

// ListModels returns the discovered servable models (interface.md §1).
func (d *daemon) ListModels() ([]dto.Model, error) {
	entries, err := d.list()
	if err != nil {
		return nil, err
	}
	out := make([]dto.Model, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.toModel())
	}
	return out, nil
}

// entryByName looks up one daemon entry by model name.
func (d *daemon) entryByName(name string) (daemonEntry, error) {
	entries, err := d.list()
	if err != nil {
		return daemonEntry{}, err
	}
	for _, e := range entries {
		if e.Name == name {
			return e, nil
		}
	}
	return daemonEntry{}, ErrModelNotFound
}

// statusOf fetches one model's live state via the `status` verb.
func (d *daemon) statusOf(name string) (dto.LiveState, error) {
	var rsp struct {
		Name  string        `json:"name"`
		State dto.LiveState `json:"state"`
	}
	if err := d.do(context.Background(), http.MethodGet, "/status/"+name, nil, &rsp); err != nil {
		return dto.LiveUnknown, err
	}
	return rsp.State, nil
}

// Resolve merges params, enforces capability gates, and folds in the fallback
// ladder (ADR-0015, interface.md §1). It reads the manifest projection and status
// exclusively through the daemon.
func (d *daemon) Resolve(name string, opts dto.ResolveOpts) (dto.Resolution, error) {
	entries, err := d.list()
	if err != nil {
		return dto.Resolution{}, err
	}
	byName := map[string]daemonEntry{}
	for _, e := range entries {
		byName[e.Name] = e
	}

	preferred, ok := byName[name]
	if !ok {
		return dto.Resolution{}, ErrModelNotFound
	}

	st, err := d.statusOf(name)
	if err != nil {
		return dto.Resolution{}, err
	}

	if st == dto.LiveUp {
		return dto.Resolution{
			Model:           preferred.toModel(),
			EffectiveParams: mergeParams(preferred.Defaults.Temperature, preferred.Defaults.MaxTokens, opts),
			LiveState:       st,
			Degraded:        false,
			UsedName:        name,
		}, nil
	}

	// Fallback ladder: walk models sharing opts.ModeTag in manifest order, first up.
	for _, cand := range entries {
		if !hasTag(cand.ModeTags, opts.ModeTag) {
			continue
		}
		cst, err := d.statusOf(cand.Name)
		if err != nil {
			continue // treat an unreadable candidate as unavailable
		}
		if cst == dto.LiveUp {
			return dto.Resolution{
				Model:           cand.toModel(),
				EffectiveParams: mergeParams(cand.Defaults.Temperature, cand.Defaults.MaxTokens, opts),
				LiveState:       dto.LiveUp,
				Degraded:        true,
				UsedName:        cand.Name,
			}, nil
		}
	}
	return dto.Resolution{}, ErrNoModelAvailable
}

// Status returns a model's live state via the daemon `status` verb.
func (d *daemon) Status(name string) (dto.LiveState, error) {
	if _, err := d.entryByName(name); err != nil {
		return dto.LiveUnknown, err
	}
	return d.statusOf(name)
}

// Start issues the daemon `start` verb (blocking up-or-typed-error). The daemon
// enforces the 60s bound and maps failures to the verb-contract error codes.
func (d *daemon) Start(name string) error {
	var rsp struct {
		Name  string `json:"name"`
		State string `json:"state"`
	}
	err := d.do(context.Background(), http.MethodPost, "/start/"+name, nil, &rsp)
	if err != nil {
		return classifyErr(err)
	}
	_ = rsp
	return nil
}

// Stop issues the daemon `stop` verb (idempotent).
func (d *daemon) Stop(name string) error {
	return d.do(context.Background(), http.MethodPost, "/stop/"+name, nil, nil)
}

// Provision issues the daemon `provision` verb (async; observable via status).
func (d *daemon) Provision(ctx context.Context, name string) (string, error) {
	var rsp struct {
		ProvisionID string `json:"provisionID"`
	}
	if err := d.do(ctx, http.MethodPost, "/provision/"+name, nil, &rsp); err != nil {
		return "", err
	}
	return rsp.ProvisionID, nil
}

// daemonError is a typed error carrying the daemon's verb-contract error code
// (interface.md §12.1).
type daemonError struct {
	code    string
	message string
	status  int
}

func (e *daemonError) Error() string {
	if e.message != "" {
		return fmt.Sprintf("%s: %s", e.code, e.message)
	}
	return e.code
}

// classifyErr maps a daemon error response to a typed Fleet error.
func classifyErr(err error) error {
	var de *daemonError
	if errors.As(err, &de) {
		switch de.code {
		case "port-in-use":
			return fmt.Errorf("%w (%s)", ErrPortInUse, de.message)
		case "binary-missing":
			return ErrBinaryMissing
		case "model-not-found", "unknown-server":
			return ErrModelNotFound
		case "not-running":
			return de // idempotent no-op surfaced as-is is acceptable for stop
		}
	}
	return err
}

// do performs one daemon request with retry/backoff (failure-semantics §1:
// daemon unreachable retries 3×), decoding the JSON response into out (if non-nil).
func (d *daemon) do(ctx context.Context, method, path string, in, out interface{}) error {
	var payload []byte
	var err error
	if in != nil {
		payload, err = json.Marshal(in)
		if err != nil {
			return err
		}
	}

	var lastErr error
	for attempt := 0; attempt <= 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff(attempt)):
			}
		}

		resp, err := d.roundTrip(ctx, method, path, payload)
		if err != nil {
			lastErr = err
			continue
		}

		if resp.status >= 200 && resp.status < 300 {
			if out != nil && len(resp.body) > 0 {
				if err := json.Unmarshal(resp.body, out); err != nil {
					return err
				}
			}
			return nil
		}

		de := &daemonError{status: resp.status}
		var ebody struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		if json.Unmarshal(resp.body, &ebody) == nil {
			de.code = ebody.Code
			de.message = ebody.Message
			if de.code == "" {
				de.code = "daemon-error"
			}
		} else {
			de.code = "daemon-error"
			de.message = string(resp.body)
		}
		// 4xx errors are not retried (mirrors the Provider's policy).
		if resp.status >= 400 && resp.status < 500 {
			return de
		}
		lastErr = de
	}
	return fmt.Errorf("%w: %v", ErrDaemonUnreachable, lastErr)
}

type daemonResp struct {
	status int
	body   []byte
}

func (d *daemon) roundTrip(ctx context.Context, method, path string, payload []byte) (daemonResp, error) {
	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, d.baseURL+path, body)
	if err != nil {
		return daemonResp{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := d.client.Do(req)
	if err != nil {
		return daemonResp{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return daemonResp{}, err
	}
	return daemonResp{status: resp.StatusCode, body: raw}, nil
}

// mergeParams merges manifest defaults ← opts.Overrides (mode.params are already
// folded into Overrides by the caller, per ADR-0026/0016). Overrides win; a zero
// override leaves the default (so a caller may override only temperature).
func mergeParams(defTemp float64, defMax int, opts dto.ResolveOpts) dto.SamplingParams {
	p := dto.SamplingParams{Temperature: defTemp, MaxTokens: defMax}
	if opts.Overrides == nil {
		return p
	}
	if opts.Overrides.Temperature != 0 {
		p.Temperature = opts.Overrides.Temperature
	}
	if opts.Overrides.MaxTokens != 0 {
		p.MaxTokens = opts.Overrides.MaxTokens
	}
	return p
}

func hasTag(tags []string, tag string) bool {
	for _, t := range tags {
		if t == tag {
			return true
		}
	}
	return false
}

func backoff(attempt int) time.Duration {
	d := time.Duration(250*(1<<(attempt-1))) * time.Millisecond
	if d > 2*time.Second {
		d = 2 * time.Second
	}
	return d
}
