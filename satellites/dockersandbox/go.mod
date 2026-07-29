// This is a SATELLITE module: its own go.mod, separate from the core cee
// module. Heavier backends live here so their dependencies never reach the
// core, which stays standard-library-only. The parent module's `go build
// ./...` does not descend into a directory that has its own go.mod, so
// nothing here can affect the core module's dependency graph.
module github.com/cee-project/cee/satellites/dockersandbox

go 1.26.5

require github.com/cee-project/cee v0.0.0

// The core module is not published; resolve it from the repo root.
replace github.com/cee-project/cee => ../..
