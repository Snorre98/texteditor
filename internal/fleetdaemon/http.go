package fleetdaemon

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"texteditor/shared/dto"
)

// errorEnvelope is the daemon's error shape (daemon-http.md §1):
// a stable `code` (interface.md §12.1) + human message.
type errorEnvelope struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// writeJSON writes a JSON response.
func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError writes an error envelope, projecting typed errors to their
// verb-contract code + HTTP status (daemon-http.md §1 status table).
func writeError(w http.ResponseWriter, err error) {
	code, status := classify(err)
	writeJSON(w, status, errorEnvelope{Code: code, Message: err.Error()})
}

func classify(err error) (code string, status int) {
	switch {
	case errorsIs(err, ErrUnknownServer):
		return "unknown-server", http.StatusNotFound
	case errorsIs(err, ErrPortInUse):
		return "port-in-use", http.StatusConflict
	case errorsIs(err, ErrModelNotFound):
		return "model-not-found", http.StatusConflict
	case errorsIs(err, ErrBinaryMissing):
		return "binary-missing", http.StatusConflict
	case errorsIs(err, ErrNotRunning):
		return "not-running", http.StatusConflict
	case errorsIs(err, ErrStartTimeout):
		return "start-timeout", http.StatusGatewayTimeout
	case errorsIs(err, ErrLanesConflict):
		return "lanes-conflict", http.StatusConflict
	default:
		// Pre-bind gate and other daemon-internal refusals are 400s.
		return "daemon-error", http.StatusBadRequest
	}
}

func errorsIs(err, target error) bool {
	return err == target || (err != nil && target != nil && strings.Contains(err.Error(), target.Error()))
}

// Handler returns the daemon's HTTP handler over the verb contract
// (contracts/daemon-http.md). It is net/http only: no ogen, no codegen — the
// daemon is a second binary on its own small surface (ADR-0032).
func (d *Control) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/list", d.handleList)
	mux.HandleFunc("/status/", d.handleStatus)
	mux.HandleFunc("/start/", d.handleStart)
	mux.HandleFunc("/stop/", d.handleStop)
	mux.HandleFunc("/provision/", d.handleProvision)
	mux.HandleFunc("/log/", d.handleLog)
	mux.HandleFunc("/reach/", d.handleReach)
	return mux
}

func (d *Control) handleList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, fmt.Errorf("method not allowed"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"models": d.List()})
}

func (d *Control) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, fmt.Errorf("method not allowed"))
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/status/")
	st, bytes, total, err := d.StatusWithProgress(name)
	if err != nil {
		writeError(w, err)
		return
	}
	resp := map[string]interface{}{"name": name, "state": string(st)}
	if st == dto.LiveProvisioning {
		resp["bytes"] = bytes
		resp["total"] = total
	}
	writeJSON(w, http.StatusOK, resp)
}

func (d *Control) handleStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, fmt.Errorf("method not allowed"))
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/start/")
	st, err := d.Start(r.Context(), name)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"name": name, "state": string(st)})
}

func (d *Control) handleStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, fmt.Errorf("method not allowed"))
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/stop/")
	st, err := d.Stop(r.Context(), name)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"name": name, "state": string(st)})
}

func (d *Control) handleProvision(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, fmt.Errorf("method not allowed"))
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/provision/")
	id, err := d.Provision(r.Context(), name)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"provisionID": id})
}

func (d *Control) handleLog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, fmt.Errorf("method not allowed"))
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/log/")
	lines, err := d.Log(name)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"name": name, "lines": lines})
}

func (d *Control) handleReach(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, fmt.Errorf("method not allowed"))
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/reach/")
	baseURL, curl, err := d.Reach(name)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"name": name, "baseURL": baseURL, "curl": curl})
}
