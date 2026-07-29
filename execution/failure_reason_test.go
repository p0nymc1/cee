package execution

import (
	"errors"
	"testing"

	"github.com/cee-project/cee/entities"
)

// A fallback step exists to handle a failure, so it is precisely the step
// that needs to know which failure it is handling. The reason used to be
// dropped whenever a fallback existed and kept only when one did not, which
// is backwards.

func TestFallbackStepLearnsWhyTheStepFailed(t *testing.T) {
	engine := NewEngine(nil)
	engine.RegisterPolicy(CircuitBreakerPolicy{PolicyID: "hold", FallbackStepRef: "held"})
	engine.RegisterWorkflow(&Workflow{
		WorkflowID:  "wf",
		EntryStepID: "risky",
		Steps: map[string]Step{
			"risky": &LeafStep{
				StepID:                  "risky",
				CircuitBreakerPolicyRef: "hold",
				Run: func(ctx map[string]any) (map[string]any, error) {
					return nil, errors.New("upstream returned 503")
				},
			},
			"held": &LeafStep{
				StepID: "held",
				Run: func(ctx map[string]any) (map[string]any, error) {
					return map[string]any{"handled": true}, nil
				},
			},
		},
	})

	result, err := engine.Run("wf", map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Output[FailureReasonKey] != "upstream returned 503" {
		t.Fatalf("the fallback should be told why, got %v", result.Output[FailureReasonKey])
	}
	if result.Output[FailedStepKey] != "risky" {
		t.Fatalf("the fallback should be told which step, got %v", result.Output[FailedStepKey])
	}
}

type reasonSandbox struct{ mode string }

func (r *reasonSandbox) Probe(entities.ProbeRequest) (entities.ProbeResult, error) {
	return entities.ProbeResult{Healthy: false, DetectedFailureMode: r.mode}, nil
}

// The case that motivated this: one fallback step reached by two different
// probe verdicts must be able to tell them apart. Otherwise a manifest that
// reports a fixed message there is confidently wrong about one of them.
func TestTwoProbeVerdictsReachingOneFallbackStayDistinct(t *testing.T) {
	build := func(mode string) *Engine {
		engine := NewEngine(&reasonSandbox{mode: mode})
		engine.RegisterPolicy(CircuitBreakerPolicy{PolicyID: "hold", FallbackStepRef: "held"})
		engine.RegisterWorkflow(&Workflow{
			WorkflowID:  "wf",
			EntryStepID: "gated",
			Steps: map[string]Step{
				"gated": &LeafStep{
					StepID:                  "gated",
					SandboxProbeRef:         "check",
					CircuitBreakerPolicyRef: "hold",
					Run: func(ctx map[string]any) (map[string]any, error) {
						return map[string]any{"wrote": true}, nil
					},
				},
				"held": &LeafStep{
					StepID: "held",
					Run: func(ctx map[string]any) (map[string]any, error) {
						return map[string]any{"outcome": "held"}, nil
					},
				},
			},
		})
		return engine
	}

	moved, err := build("row moved from 3 to 7").Run("wf", map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	missing, err := build("target has no such row").Run("wf", map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Both take the same path to the same fallback...
	if moved.Output["outcome"] != "held" || missing.Output["outcome"] != "held" {
		t.Fatalf("expected both to reach the fallback, got %v and %v", moved.Output, missing.Output)
	}
	// ...but must not be indistinguishable once there.
	if moved.Output[FailureReasonKey] == missing.Output[FailureReasonKey] {
		t.Fatalf("two different probe verdicts collapsed into one reason: %v",
			moved.Output[FailureReasonKey])
	}
	if moved.Output[FailureReasonKey] != "row moved from 3 to 7" {
		t.Fatalf("unexpected reason: %v", moved.Output[FailureReasonKey])
	}
	// The gated action must not have run in either case.
	if moved.Output["wrote"] == true || missing.Output["wrote"] == true {
		t.Fatal("a blocked step must not have executed")
	}
}

// The keys are only written when a breaker actually diverts, so a clean run
// carries no engine bookkeeping in its output.
func TestSuccessfulRunCarriesNoFailureKeys(t *testing.T) {
	engine := NewEngine(nil)
	engine.RegisterWorkflow(&Workflow{
		WorkflowID:  "wf",
		EntryStepID: "fine",
		Steps: map[string]Step{
			"fine": &LeafStep{
				StepID: "fine",
				Run: func(ctx map[string]any) (map[string]any, error) {
					return map[string]any{"ok": true}, nil
				},
			},
		},
	})

	result, err := engine.Run("wf", map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, present := result.Output[FailureReasonKey]; present {
		t.Fatal("a run that never failed must not carry a failure reason")
	}
	if _, present := result.Output[FailedStepKey]; present {
		t.Fatal("a run that never failed must not carry a failed step")
	}
}

// StatePointer answers exactly one question: is this run parked? It used to
// echo the workflow ref on completion, which made the obvious check wrong for
// every run that finished normally.
func TestStatePointerIsEmptyUnlessParked(t *testing.T) {
	store := NewMemoryStore()
	engine := NewEngine(nil)
	engine.SetStore(store)
	engine.RegisterWorkflow(&Workflow{
		WorkflowID:  "wf",
		EntryStepID: "start",
		Steps: map[string]Step{
			"start": &LeafStep{
				StepID: "start",
				Run: func(ctx map[string]any) (map[string]any, error) {
					if ctx["park"] == true {
						return Suspend("waiting")
					}
					return map[string]any{"done": true}, nil
				},
			},
		},
	})

	finished, err := engine.Run("wf", map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if finished.StatePointer != "" {
		t.Fatalf("a completed run has nothing to resume, got %q", finished.StatePointer)
	}

	parked, err := engine.Run("wf", map[string]any{"park": true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if parked.StatePointer == "" {
		t.Fatal("a parked run must carry a pointer")
	}
}
