package fleetdaemon

import (
	"errors"
	"log"
	"os/exec"
)

// runnerCommand is the hidden seam between the daemon and serve.sh: it builds the
// *exec.Cmd to launch one server, given the parsed manifest fields handed to
// serve.sh as per-invocation env vars (ADR-0032 §3). serve.sh is the lifecycle
// executor; the daemon wraps it (ADR-0025 §1).
//
// Swappable in tests via Control.RunnerCommand; the production value shell-outs to
// the on-disk serve.sh.
type runnerCommand func(r runnerSpec) (*exec.Cmd, error)

// runnerSpec is the per-invocation manifest projection handed to serve.sh: the
// env vars RUNNER / MODEL / HOST / PORT / SERVE_PORT_<NAME>, plus the resolved
// absolute path to the serve.sh script and the repo root it lives in.
type runnerSpec struct {
	Runner   string // llama.cpp | mlx-lm | mlx-vlm | delegate
	Model    string // HF repo id (mlx-*) or GGUF file (llama.cpp) or "" (delegate)
	Delegate string // wrapper script name when runner == delegate
	Host     string
	Port     int
	// ServeSh is the absolute path to the serve.sh script the daemon wraps.
	ServeSh string
}

// commandRunner returns a runnerCommand that invokes serve.sh <start> for one
// server, forwarding the parsed manifest as env vars (ADR-0032 §3). The daemon
// never re-implements runner launch logic (ADR-0025 §1).
func commandRunner() runnerCommand {
	return func(r runnerSpec) (*exec.Cmd, error) {
		if r.ServeSh == "" {
			return nil, errors.New("serve.sh path not configured")
		}
		cmd := exec.Command(r.ServeSh, "start", r.modelLabel())
		cmd.Env = append(cmd.Env,
			"RUNNER="+r.Runner,
			"MODEL="+r.Model,
			"HOST="+r.Host,
			"PORT="+itoa(r.Port),
			"DELEGATE="+r.Delegate,
		)
		return cmd, nil
	}
}

func (r runnerSpec) modelLabel() string {
	return r.Runner
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// logf is the daemon's own logger (its log is distinct from runner logs,
// ADR-0032 §6).
var logf = log.Printf
