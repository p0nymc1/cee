// Package wasmhooks lets a domain plugin's behavior be untrusted, third-party
// code by running it as WebAssembly behind a hard boundary, exposed to the
// core as an ordinary execution.Action (i.e. a manifest Hook).
//
// The trust boundary is the whole point. A WASM module gets exactly one thing
// in -- the step context, marshalled to JSON -- and returns exactly one thing
// out -- a JSON object that becomes the step output. It has no access to the
// host filesystem, network, environment, or memory. So a plugin author you do
// not trust can still contribute executable logic without being able to reach
// anything but the data you hand the step.
//
// This satellite lives in its own module (see go.mod) so the WASM runtime
// dependency never reaches the core. The Runtime interface below is the seam:
// production wires a wazero-backed Runtime (pure Go, added as a require in
// THIS module); the core stays standard-library-only.
package wasmhooks

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/p0nymc1/cee/execution"
)

// Runtime executes a WebAssembly module in isolation: inputJSON in, outputJSON
// out, nothing else crosses. A production implementation is a thin adapter
// over a WASM runtime such as wazero; tests inject a fake. The context lets
// the caller bound execution time so a runaway module cannot hang a step.
type Runtime interface {
	Call(ctx context.Context, module []byte, inputJSON []byte) (outputJSON []byte, err error)
}

// Hook wraps an untrusted WASM module as a core execution.Action. Register it
// in a manifest.Hooks map under an action_ref and a plugin step runs
// sandboxed third-party code with no change to the engine.
func Hook(rt Runtime, module []byte) execution.Action {
	return hook(rt, module, 0)
}

// HookWithTimeout is Hook with a wall-clock ceiling on module execution.
// A module that does not return in time fails the step (which the engine then
// routes through the step's circuit breaker like any other failure), rather
// than hanging the run.
func HookWithTimeout(rt Runtime, module []byte, timeout time.Duration) execution.Action {
	return hook(rt, module, timeout)
}

func hook(rt Runtime, module []byte, timeout time.Duration) execution.Action {
	return func(stepCtx map[string]any) (map[string]any, error) {
		inputJSON, err := json.Marshal(stepCtx)
		if err != nil {
			return nil, fmt.Errorf("wasmhooks: cannot marshal step context: %w", err)
		}

		ctx := context.Background()
		if timeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, timeout)
			defer cancel()
		}

		outputJSON, err := rt.Call(ctx, module, inputJSON)
		if err != nil {
			return nil, fmt.Errorf("wasmhooks: module execution failed: %w", err)
		}

		var out map[string]any
		if err := json.Unmarshal(outputJSON, &out); err != nil {
			return nil, fmt.Errorf("wasmhooks: module returned invalid JSON: %w", err)
		}
		return out, nil
	}
}
