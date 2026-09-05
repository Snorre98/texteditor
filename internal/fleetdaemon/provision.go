package fleetdaemon

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"texteditor/shared/dto"
)

// provisionJob is one in-flight (or last) HF download for a model.
type provisionJob struct {
	provisionID string
	done        chan struct{}
	err         error
}

// Provision kicks off an async HF download for one model (daemon-http.md §2,
// ADR-0008). It returns immediately with a provisionID; progress is observed via
// Status (state provisioning + bytes/total). Re-running skips already-present
// files (huggingface-cli resumes by content).
func (d *Control) Provision(ctx context.Context, name string) (string, error) {
	mo, _, ok := d.manifest.modelByName(name)
	if !ok {
		return "", ErrUnknownServer
	}

	// only hf / gguf sources are provisionable (ADR-0030 §3; needle is drop-shipped).
	if mo.Source.Kind != "hf" && mo.Source.Kind != "gguf" {
		return "", fmt.Errorf("%w: source.kind %q is not provisionable", ErrModelNotFound, mo.Source.Kind)
	}

	// Coalesce: an in-flight provision reuses its job.
	d.mu.Lock()
	if j, inflight := d.provision[name]; inflight {
		d.mu.Unlock()
		select {
		case <-j.done:
		case <-ctx.Done():
			return "", ctx.Err()
		}
		return j.provisionID, nil
	}
	job := &provisionJob{provisionID: "prov-" + name, done: make(chan struct{})}
	d.provision[name] = job
	d.mu.Unlock()

	// Progress is reported by huggingface-cli on stderr but not intercepted here;
	// the daemon marks the model provisioning with an unknown total (0 total means
	// "in progress, size unknown") observable via Status bytes/total.
	d.states.setProgress(name, 0, 0)

	go func() {
		job.err = d.download(name, mo)
		// Either way the download ends in `down` (state-machine.md §2.2), then a
		// start becomes possible once the weights are on disk.
		d.states.set(name, dto.LiveDown)
		close(job.done)
		d.mu.Lock()
		delete(d.provision, name)
		d.mu.Unlock()
	}()

	return job.provisionID, nil
}

// download shells huggingface-cli to fetch the source (ADR-0008 §1). It is the
// daemon's provision verb; progress is left to the huggingface-cli's resume-by-
// content behavior rather than a re-implemented byte counter.
func (d *Control) download(name string, mo Model) error {
	var args []string
	switch mo.Source.Kind {
	case "hf":
		args = []string{"download", mo.Source.Repo}
	case "gguf":
		args = []string{"download", mo.Source.File}
	}
	cmd := exec.Command("huggingface-cli", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: provision failed: %v (%s)", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}
