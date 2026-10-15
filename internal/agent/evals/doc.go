// Package evals runs the consultant against the real model and a real,
// seeded database, and checks what it does with real buyer messages --
// including hostile ones.
//
// The tests cost money and need ANTHROPIC_API_KEY and Docker, so they sit
// behind the "eval" build tag: `go test ./...` and CI never run them. Run them
// with `make eval`.
package evals
