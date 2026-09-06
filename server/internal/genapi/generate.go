// Package genapi authors the engine's OpenAPI contract and regenerates the ogen
// server. The spec (api/openapi.yaml, ADR-0017) is the single source of truth for
// all three clients (ogen Go server, Hey API + Zod TS, openapi-to-rust Rust),
// with the `/turn` SSE endpoint hand-framed via x-ogen-raw-response (ADR-0031).
//
// Regenerate the Go server with:
//
//	go generate ./...
package genapi

//go:generate go run texteditor/internal/ogen
