# wasmhooks — untrusted hooks as sandboxed WebAssembly

A satellite module (own `go.mod`) that lets a domain plugin's behavior be
**untrusted, third-party code**, run as WebAssembly behind a hard boundary and
exposed to the core CEE engine as an ordinary `execution.Action` (a manifest
Hook).

## The boundary

A WASM module gets exactly one input — the step context as JSON — and returns
exactly one output — a JSON object that becomes the step output. It has **no
access** to the host filesystem, network, environment, or memory. That is what
lets you accept executable logic from a contributor you don't trust: the worst
a module can do is compute a wrong answer from the data you handed it.

```go
hooks := manifest.Hooks{
    "untrusted.score": wasmhooks.HookWithTimeout(runtime, moduleBytes, 100*time.Millisecond),
}
domain, _ := manifest.Load(manifestJSON, hooks, stdlib.Default())
```

## What's here vs. what you add

This module is complete and tested **offline** for everything that touches
CEE: the boundary contract, the `execution.Action` integration, timeout
handling, and the end-to-end path where an untrusted module backs a real
manifest step (`wasmhooks_test.go`). Tests use a fake `Runtime`, so no wasm
toolchain is needed.

The one production piece is the `Runtime` implementation that actually executes
wasm bytes. Wire it with [wazero](https://github.com/tetratelabs/wazero) — a
pure-Go WASM runtime with no further transitive dependencies — added as a
`require` **in this module's `go.mod`, never in the core**:

```go
// runtime_wazero.go (add `require github.com/tetratelabs/wazero` first)
type wazeroRuntime struct{ r wazero.Runtime }

func (w *wazeroRuntime) Call(ctx context.Context, module, inputJSON []byte) ([]byte, error) {
    // instantiate `module`, write inputJSON into its linear memory, call the
    // exported entrypoint, read outputJSON back out, return it. wazero grants
    // the module no host access unless you explicitly export functions to it —
    // so exporting nothing is the sandbox.
}
```

`Runtime` is the seam: everything else in CEE stays standard-library-only, and
the core engine never learns wasm exists.
