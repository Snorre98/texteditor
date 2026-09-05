// Command ogen-generate generates the ogen OpenAPI server package from
// api/openapi.yaml into internal/genapi. Run via `go generate ./...` or directly
// with `go run ./internal/ogen`.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/ogen-go/ogen"
	"github.com/ogen-go/ogen/gen"
	"github.com/ogen-go/ogen/gen/genfs"
)

func main() {
	// Resolve the repo root from this source file, so `go generate` works from
	// any package directory (the generator may run with CWD = the package dir).
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		fatal(fmt.Errorf("cannot locate source file"))
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))

	specPath := filepath.Join(root, "api", "openapi.yaml")
	targetDir := filepath.Join(root, "internal", "genapi")

	opts := gen.Options{}
	data, err := opts.SetLocation(specPath, gen.RemoteOptions{})
	if err != nil {
		fatal(fmt.Errorf("spec: %w", err))
	}
	opts.Parser.InferSchemaType = true

	spec, err := ogen.Parse(data)
	if err != nil {
		fatal(fmt.Errorf("parse spec: %w", err))
	}

	g, err := gen.NewGenerator(spec, opts)
	if err != nil {
		fatal(fmt.Errorf("build IR: %w", err))
	}

	fs := genfs.FormattedSource{Root: targetDir}
	if err := g.WriteSource(fs, "genapi"); err != nil {
		fatal(fmt.Errorf("write: %w", err))
	}

	fmt.Println("generated ogen server into", targetDir)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "ogen-gen:", err)
	os.Exit(1)
}
