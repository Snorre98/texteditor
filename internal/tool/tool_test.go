package tool

import (
	"encoding/json"
	"testing"

	"texteditor/shared/dto"
)

func TestRegistryRegisterListAllowlist(t *testing.T) {
	r := NewRegistry()

	submit := dto.ToolDef{Name: "submit", Description: "s", Parameters: json.RawMessage(`{"type":"object"}`)}
	retrieve := dto.ToolDef{Name: "retrieve", Description: "r", Parameters: json.RawMessage(`{"type":"object"}`)}

	if err := r.Register(submit); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(retrieve); err != nil {
		t.Fatal(err)
	}

	// Duplicate rejected.
	if err := r.Register(submit); err != ErrDuplicate {
		t.Fatalf("duplicate register: got %v want ErrDuplicate", err)
	}

	// Reserved name rejected (ADR-0028 §2).
	if err := r.Register(dto.ToolDef{Name: ReservedRequestToolName}); err != ErrReservedName {
		t.Fatalf("reserved name: got %v want ErrReservedName", err)
	}

	// List is stable/sorted.
	list := r.List()
	if len(list) != 2 {
		t.Fatalf("List len = %d, want 2", len(list))
	}
	if list[0].Name != "retrieve" || list[1].Name != "submit" {
		t.Fatalf("List order = %v, want [retrieve submit]", list)
	}

	// AllowlistFor preserves the mode's order and returns only known tools.
	mode := dto.Mode{ToolAllowlist: []string{"submit", "retrieve"}}
	al := r.AllowlistFor(mode)
	if len(al) != 2 || al[0].Name != "submit" || al[1].Name != "retrieve" {
		t.Fatalf("AllowlistFor order = %v", al)
	}

	// Unknown names are simply omitted.
	mode2 := dto.Mode{ToolAllowlist: []string{"nope"}}
	if got := r.AllowlistFor(mode2); len(got) != 0 {
		t.Fatalf("AllowlistFor unknown returned %v", got)
	}
}

func TestExecutorNameKeyedInvoke(t *testing.T) {
	e := NewExecutor()

	e.Bind("echo", func(args json.RawMessage) (json.RawMessage, error) {
		return args, nil
	})

	out, err := e.Invoke("echo", json.RawMessage(`{"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"a":1}` {
		t.Fatalf("echo returned %s", out)
	}

	if _, err := e.Invoke("nohandler", nil); err != ErrToolNoHandler {
		t.Fatalf("got %v want ErrToolNoHandler", err)
	}
}
