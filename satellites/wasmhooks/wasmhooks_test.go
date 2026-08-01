package wasmhooks

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/p0nymc1/cee/execution"
	"github.com/p0nymc1/cee/intentrouter"
	"github.com/p0nymc1/cee/manifest"
	"github.com/p0nymc1/cee/registry"
	"github.com/p0nymc1/cee/stdlib"
)

type fakeRuntime struct {
	module func(in map[string]any) map[string]any
	err    error
	block  bool
}

func (f *fakeRuntime) Call(ctx context.Context, module []byte, inputJSON []byte) ([]byte, error) {
	if f.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if f.err != nil {
		return nil, f.err
	}
	var in map[string]any
	if err := json.Unmarshal(inputJSON, &in); err != nil {
		return nil, err
	}
	return json.Marshal(f.module(in))
}

var _ execution.Action = Hook(&fakeRuntime{module: func(map[string]any) map[string]any { return nil }}, nil)

func TestHookRunsModuleAcrossTheBoundary(t *testing.T) {
	rt := &fakeRuntime{module: func(in map[string]any) map[string]any {

		return map[string]any{"doubled": in["amount"].(float64) * 2}
	}}
	action := Hook(rt, []byte("pretend-wasm-bytes"))

	out, err := action(map[string]any{"amount": 21.0})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out["doubled"] != 42.0 {
		t.Fatalf("expected doubled=42, got %+v", out)
	}
}

func TestModuleErrorBecomesStepError(t *testing.T) {
	action := Hook(&fakeRuntime{err: errors.New("trap: out of bounds")}, nil)
	if _, err := action(map[string]any{}); err == nil {
		t.Fatalf("expected a module trap to surface as a step error")
	}
}

func TestInvalidModuleOutputIsRejected(t *testing.T) {

	rt := runtimeReturning([]byte("not json"))
	if _, err := Hook(rt, nil)(map[string]any{}); err == nil {
		t.Fatalf("expected invalid module output to be rejected")
	}
}

func TestTimeoutStopsARunawayModule(t *testing.T) {
	action := HookWithTimeout(&fakeRuntime{block: true}, nil, 20*time.Millisecond)
	start := time.Now()
	if _, err := action(map[string]any{}); err == nil {
		t.Fatalf("expected a timeout error from a runaway module")
	}
	if time.Since(start) > time.Second {
		t.Fatalf("timeout did not bound execution")
	}
}

func TestUntrustedModuleBacksAManifestStep(t *testing.T) {
	rt := &fakeRuntime{module: func(in map[string]any) map[string]any {
		return map[string]any{"score": in["amount"].(float64) + 1}
	}}
	hooks := manifest.Hooks{
		"untrusted.score": Hook(rt, []byte("module")),
	}
	manifestJSON := []byte(`{
		"name": "risk",
		"workflows": [{
			"workflow_id": "risk.score",
			"entry_step_id": "compute",
			"steps": [{"step_id": "compute", "type": "leaf", "action_ref": "untrusted.score"}]
		}]
	}`)

	domain, err := manifest.Load(manifestJSON, hooks, stdlib.Default())
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	router := intentrouter.NewRouter(0.3)
	engine := execution.NewEngine(nil)
	registry.NewRegistry(router, engine).RegisterDomain(*domain)

	result, err := engine.Run("risk.score", map[string]any{"amount": 9.0})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Output["score"] != 10.0 {
		t.Fatalf("expected score=10 from the sandboxed module, got %+v", result.Output)
	}
}

func runtimeReturning(b []byte) Runtime {
	return &staticRuntime{out: b}
}

type staticRuntime struct{ out []byte }

func (s *staticRuntime) Call(context.Context, []byte, []byte) ([]byte, error) {
	return s.out, nil
}

type deadlineProbe struct{ hadDeadline bool }

func (d *deadlineProbe) Call(ctx context.Context, module, inputJSON []byte) ([]byte, error) {
	_, d.hadDeadline = ctx.Deadline()
	return []byte(`{}`), nil
}

func TestHookRunsUntrustedCodeUnderADeadline(t *testing.T) {
	p := &deadlineProbe{}
	if _, err := Hook(p, nil)(map[string]any{}); err != nil {
		t.Fatalf("Hook: %v", err)
	}
	if !p.hadDeadline {
		t.Error("Hook must run an untrusted module under a deadline, not context.Background()")
	}
}

func TestHookWithTimeoutZeroDisablesTheDeadlineDeliberately(t *testing.T) {
	p := &deadlineProbe{}
	if _, err := HookWithTimeout(p, nil, 0)(map[string]any{}); err != nil {
		t.Fatalf("HookWithTimeout: %v", err)
	}
	if p.hadDeadline {
		t.Error("an explicit 0 timeout must disable the deadline")
	}
}
