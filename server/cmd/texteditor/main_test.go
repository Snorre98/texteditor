package main

import (
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestEnvOr(t *testing.T) {
	key := "TEXTEDITOR_TEST_ENVOR"
	t.Cleanup(func() { os.Unsetenv(key) })

	if got := envOr(key, "default"); got != "default" {
		t.Fatalf("envOr(unset) = %q, want default", got)
	}
	os.Setenv(key, "custom")
	if got := envOr(key, "default"); got != "custom" {
		t.Fatalf("envOr(set) = %q, want custom", got)
	}
}

func TestEnvInt(t *testing.T) {
	key := "TEXTEDITOR_TEST_ENVINT"
	t.Cleanup(func() { os.Unsetenv(key) })

	if got := envInt(key, 42); got != 42 {
		t.Fatalf("envInt(unset) = %d, want 42", got)
	}
	os.Setenv(key, "9100")
	if got := envInt(key, 42); got != 9100 {
		t.Fatalf("envInt(set) = %d, want 9100", got)
	}
	os.Setenv(key, "not-a-number")
	if got := envInt(key, 42); got != 42 {
		t.Fatalf("envInt(garbage) = %d, want fallback 42", got)
	}
}

func TestSplitList(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"tauri://localhost", []string{"tauri://localhost"}},
		{"tauri://localhost, http://localhost:5173", []string{"tauri://localhost", "http://localhost:5173"}},
		{" a , , b ", []string{"a", "b"}},
	}
	for _, c := range cases {
		got := splitList(c.in)
		if len(got) != len(c.want) {
			t.Fatalf("splitList(%q) = %v, want %v", c.in, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("splitList(%q) = %v, want %v", c.in, got, c.want)
			}
		}
	}
}

func TestBindListenerDynamic(t *testing.T) {
	ln, baseURL, err := bindListener("127.0.0.1", 0)
	if err != nil {
		t.Fatalf("bindListener(dynamic): %v", err)
	}
	defer ln.Close()

	host, port, err := net.SplitHostPort(strings.TrimPrefix(baseURL, "http://"))
	if err != nil {
		t.Fatalf("baseURL %q not host:port: %v", baseURL, err)
	}
	if host != "127.0.0.1" {
		t.Fatalf("baseURL host = %q, want 127.0.0.1", host)
	}
	if n, _ := strconv.Atoi(port); n == 0 {
		t.Fatalf("dynamic port resolved to 0, want an OS-assigned free port")
	}
}

func TestBindListenerFixed(t *testing.T) {
	// Find a free port, then bind it fixed.
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("probe listen: %v", err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	probe.Close()

	ln, baseURL, err := bindListener("127.0.0.1", port)
	if err != nil {
		t.Fatalf("bindListener(fixed %d): %v", port, err)
	}
	defer ln.Close()
	if want := "http://127.0.0.1:" + strconv.Itoa(port); baseURL != want {
		t.Fatalf("baseURL = %q, want %q", baseURL, want)
	}
}
