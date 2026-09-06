package routergate

import (
	"encoding/json"
	"errors"
	"testing"

	"texteditor/shared/dto"
)

func toolDef(name string) dto.ToolDef {
	return dto.ToolDef{Name: name, Description: "d " + name, Parameters: json.RawMessage(`{"type":"object","properties":{}}`)}
}

func TestToolSetHashDeterministic(t *testing.T) {
	defs := []dto.ToolDef{toolDef("edit_markdown"), toolDef("retrieve"), toolDef("diff")}
	h1 := ToolSetHash(defs)
	h2 := ToolSetHash([]dto.ToolDef{toolDef("diff"), toolDef("edit_markdown"), toolDef("retrieve")})
	if h1 != h2 {
		t.Fatalf("hash not order-independent: %q vs %q", h1, h2)
	}
	h3 := ToolSetHash(defs)
	if h1 != h3 {
		t.Fatalf("hash not deterministic: %q vs %q", h1, h3)
	}
}

func TestToolSetHashChangeSensitive(t *testing.T) {
	base := []dto.ToolDef{toolDef("retrieve")}
	h := ToolSetHash(base)

	renamed := []dto.ToolDef{{Name: "retrieve2", Description: "d retrieve", Parameters: json.RawMessage(`{"type":"object","properties":{}}`)}}
	if ToolSetHash(renamed) == h {
		t.Fatal("rename did not change the hash")
	}
	reworded := []dto.ToolDef{{Name: "retrieve", Description: "changed", Parameters: json.RawMessage(`{"type":"object","properties":{}}`)}}
	if ToolSetHash(reworded) == h {
		t.Fatal("description change did not change the hash")
	}
	retyped := []dto.ToolDef{{Name: "retrieve", Description: "d retrieve", Parameters: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}}}`)}}
	if ToolSetHash(retyped) == h {
		t.Fatal("schema change did not change the hash")
	}
}

func TestToolSetHashSchemaFormatInsensitive(t *testing.T) {
	spaced := []dto.ToolDef{{
		Name: "retrieve", Description: "d",
		Parameters: json.RawMessage(` { "type" : "object" } `),
	}}
	compact := []dto.ToolDef{{
		Name: "retrieve", Description: "d",
		Parameters: json.RawMessage(`{"type":"object"}`),
	}}
	if ToolSetHash(spaced) != ToolSetHash(compact) {
		t.Fatal("schema whitespace changed the hash; canonicalization failed")
	}
}

func TestCheck(t *testing.T) {
	routerMode := dto.Mode{Name: "editor", ToolCalling: "router"}
	nativeMode := dto.Mode{Name: "proofreader", ToolCalling: "native"}
	hash := ToolSetHash([]dto.ToolDef{toolDef("retrieve")})

	t.Run("no router mode is a no-op", func(t *testing.T) {
		err := Check([]dto.Mode{nativeMode},
			func(string) bool { return false },
			func(string) (string, error) { return "", nil },
			"whatever")
		if err != nil {
			t.Fatalf("want nil for an all-native fleet, got %v", err)
		}
	})

	t.Run("router mode without needle-router", func(t *testing.T) {
		err := Check([]dto.Mode{routerMode},
			func(string) bool { return false },
			func(string) (string, error) { return "", nil },
			hash)
		if !errors.Is(err, ErrRouterUnavailable) {
			t.Fatalf("want ErrRouterUnavailable, got %v", err)
		}
	})

	t.Run("matching fingerprint passes", func(t *testing.T) {
		err := Check([]dto.Mode{routerMode},
			func(name string) bool { return name == RouterModelName },
			func(name string) (string, error) { return hash, nil },
			hash)
		if err != nil {
			t.Fatalf("want nil, got %v", err)
		}
	})

	t.Run("mismatched fingerprint", func(t *testing.T) {
		err := Check([]dto.Mode{routerMode},
			func(name string) bool { return name == RouterModelName },
			func(name string) (string, error) { return "sha256:other", nil },
			hash)
		if !errors.Is(err, ErrToolsStale) {
			t.Fatalf("want ErrToolsStale, got %v", err)
		}
	})

	t.Run("missing fingerprint is stale", func(t *testing.T) {
		err := Check([]dto.Mode{routerMode},
			func(name string) bool { return name == RouterModelName },
			func(name string) (string, error) { return "", nil },
			hash)
		if !errors.Is(err, ErrToolsStale) {
			t.Fatalf("want ErrToolsStale for an absent fingerprint, got %v", err)
		}
	})

	t.Run("fingerprint read failure is router-unavailable", func(t *testing.T) {
		err := Check([]dto.Mode{routerMode},
			func(name string) bool { return name == RouterModelName },
			func(name string) (string, error) { return "", errors.New("daemon down") },
			hash)
		if !errors.Is(err, ErrRouterUnavailable) {
			t.Fatalf("want ErrRouterUnavailable wrapping the read error, got %v", err)
		}
	})
}
