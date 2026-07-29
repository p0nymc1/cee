// SATELLITE module: untrusted third-party hooks run as sandboxed WebAssembly.
// Its own go.mod keeps any WASM-runtime dependency out of the core cee
// module, which stays standard-library-only.
//
// This module defines the trust boundary and the engine integration with the
// standard library alone, so it builds and tests offline. The production
// Runtime is a thin adapter over a WASM runtime such as wazero (pure Go, no
// further transitive dependencies); when wiring that in, add its require here
// -- in the satellite, never in the core. See README.md.
module github.com/cee-project/cee/satellites/wasmhooks

go 1.26.5

require github.com/cee-project/cee v0.0.0

replace github.com/cee-project/cee => ../..
