package fleet

import (
	"context"
	"errors"
	"testing"

	"texteditor/shared/dto"
)

func newStubFleet() Interface {
	return NewStub([]dto.Model{
		{Name: "gemma4-12b", BaseURL: "http://localhost:8001/v1", ModeTags: []string{"proofreader", "editor"}},
		{Name: "gemma4-26b", BaseURL: "http://localhost:8002/v1", ModeTags: []string{"editor"}},
		{Name: "llama3.1-8b", BaseURL: "http://localhost:8003/v1", ModeTags: []string{"grammar"}},
	})
}

func TestResolveDirect(t *testing.T) {
	f := newStubFleet()
	params := dto.SamplingParams{Temperature: 0.3, MaxTokens: 10}
	res, err := f.Resolve("gemma4-12b", dto.ResolveOpts{ModeTag: "proofreader", Overrides: &params})
	if err != nil {
		t.Fatal(err)
	}
	if res.UsedName != "gemma4-12b" || res.Degraded {
		t.Fatalf("resolution = %+v, want direct", res)
	}
	if res.EffectiveParams.Temperature != 0.3 {
		t.Fatalf("effective params = %+v", res.EffectiveParams)
	}
}

func TestResolveFallbackLadder(t *testing.T) {
	f := newStubFleet()
	if err := f.Stop("gemma4-12b"); err != nil {
		t.Fatal(err)
	}

	// Preferred is down; modeTag "editor" is shared by gemma4-26b (up) → fallback.
	res, err := f.Resolve("gemma4-12b", dto.ResolveOpts{ModeTag: "editor"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Degraded || res.UsedName != "gemma4-26b" {
		t.Fatalf("resolution = %+v, want degraded fallback to gemma4-26b", res)
	}
}

func TestResolveNoModelAvailable(t *testing.T) {
	f := newStubFleet()
	_ = f.Stop("gemma4-12b")
	_ = f.Stop("gemma4-26b")

	_, err := f.Resolve("gemma4-12b", dto.ResolveOpts{ModeTag: "editor"})
	if !errors.Is(err, ErrNoModelAvailable) {
		t.Fatalf("want ErrNoModelAvailable, got %v", err)
	}
}

func TestResolveUnknownModel(t *testing.T) {
	f := newStubFleet()
	_, err := f.Resolve("nope", dto.ResolveOpts{})
	if !errors.Is(err, ErrModelNotFound) {
		t.Fatalf("want ErrModelNotFound, got %v", err)
	}
}

func TestNoDaemonFailsLoudly(t *testing.T) {
	f := NewDaemon("http://localhost:9999")
	if _, err := f.ListModels(); !errors.Is(err, ErrDaemonUnreachable) {
		t.Fatalf("want ErrDaemonUnreachable, got %v", err)
	}
	if _, err := f.Resolve("x", dto.ResolveOpts{}); !errors.Is(err, ErrDaemonUnreachable) {
		t.Fatalf("want ErrDaemonUnreachable, got %v", err)
	}
}

func TestProvision(t *testing.T) {
	f := newStubFleet()
	id, err := f.Provision(context.Background(), "gemma4-12b")
	if err != nil {
		t.Fatal(err)
	}
	if id == "" {
		t.Fatal("empty provision id")
	}
	if s, _ := f.Status("gemma4-12b"); s != dto.LiveProvisioning {
		t.Fatalf("status = %s, want provisioning", s)
	}
}

func TestListModels(t *testing.T) {
	f := newStubFleet()
	models, err := f.ListModels()
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 3 {
		t.Fatalf("models = %d, want 3", len(models))
	}
}

func TestStartStop(t *testing.T) {
	f := newStubFleet()
	if err := f.Start("gemma4-12b"); err != nil {
		t.Fatal(err)
	}
	if s, _ := f.Status("gemma4-12b"); s != dto.LiveUp {
		t.Fatalf("status = %s, want up", s)
	}
	if err := f.Stop("gemma4-12b"); err != nil {
		t.Fatal(err)
	}
	if s, _ := f.Status("gemma4-12b"); s != dto.LiveDown {
		t.Fatalf("status = %s, want down", s)
	}
	// Unknown name is typed.
	if err := f.Start("nope"); !errors.Is(err, ErrModelNotFound) {
		t.Fatalf("want ErrModelNotFound, got %v", err)
	}
}
