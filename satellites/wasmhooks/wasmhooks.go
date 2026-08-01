package wasmhooks

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/p0nymc1/cee/execution"
)

type Runtime interface {
	Call(ctx context.Context, module []byte, inputJSON []byte) (outputJSON []byte, err error)
}

// DefaultTimeout bounds how long an untrusted module may run under Hook. A
// package whose whole purpose is a trust boundary should not default to running
// unknown code with no time limit, so Hook applies this and HookWithTimeout is
// the way to choose another value -- or 0 to disable it deliberately.
const DefaultTimeout = 5 * time.Second

func Hook(rt Runtime, module []byte) execution.Action {
	return hook(rt, module, DefaultTimeout)
}

// HookWithTimeout runs the module under the given timeout. A timeout of 0
// disables the deadline -- an explicit choice, unlike Hook's safe default.
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
