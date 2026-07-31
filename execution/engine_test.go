package execution

import (
	"errors"
	"testing"

	"github.com/p0nymc1/cee/entities"
)

func TestRunsLinearLeafSteps(t *testing.T) {
	engine := NewEngine(nil)
	engine.RegisterWorkflow(&Workflow{
		WorkflowID:  "double_then_add",
		EntryStepID: "double",
		Steps: map[string]Step{
			"double": &LeafStep{
				StepID: "double",
				Run: func(ctx map[string]any) (map[string]any, error) {
					return map[string]any{"value": ctx["value"].(int) * 2}, nil
				},
				OnSuccess: "add_one",
			},
			"add_one": &LeafStep{
				StepID: "add_one",
				Run: func(ctx map[string]any) (map[string]any, error) {
					return map[string]any{"value": ctx["value"].(int) + 1}, nil
				},
			},
		},
	})

	result, err := engine.Run("double_then_add", map[string]any{"value": 5})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Output["value"] != 11 {
		t.Fatalf("expected 11, got %v", result.Output["value"])
	}
	if len(result.Trace) != 2 || result.Trace[0] != "double" || result.Trace[1] != "add_one" {
		t.Fatalf("unexpected trace: %v", result.Trace)
	}
}

func TestCompositeStepRunsSubWorkflow(t *testing.T) {
	engine := NewEngine(nil)
	engine.RegisterWorkflow(&Workflow{
		WorkflowID:  "inner",
		EntryStepID: "inc",
		Steps: map[string]Step{
			"inc": &LeafStep{
				StepID: "inc",
				Run: func(ctx map[string]any) (map[string]any, error) {
					return map[string]any{"value": ctx["value"].(int) + 1}, nil
				},
			},
		},
	})
	engine.RegisterWorkflow(&Workflow{
		WorkflowID:  "outer",
		EntryStepID: "call_inner",
		Steps: map[string]Step{
			"call_inner": &CompositeStep{StepID: "call_inner", SubWorkflowRef: "inner"},
		},
	})

	result, err := engine.Run("outer", map[string]any{"value": 1})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Output["value"] != 2 {
		t.Fatalf("expected 2, got %v", result.Output["value"])
	}
}

func TestFailureWithoutPolicyTripsCircuitBreaker(t *testing.T) {
	engine := NewEngine(nil)
	engine.RegisterWorkflow(&Workflow{
		WorkflowID:  "fragile",
		EntryStepID: "explode",
		Steps: map[string]Step{
			"explode": &LeafStep{
				StepID: "explode",
				Run: func(ctx map[string]any) (map[string]any, error) {
					return nil, errors.New("boom")
				},
			},
		},
	})

	_, err := engine.Run("fragile", map[string]any{})
	var tripped *CircuitBreakerTripped
	if !errors.As(err, &tripped) {
		t.Fatalf("expected CircuitBreakerTripped, got %v", err)
	}
}

func TestFailureWithPolicyFallsBack(t *testing.T) {
	engine := NewEngine(nil)
	engine.RegisterPolicy(CircuitBreakerPolicy{PolicyID: "retry_elsewhere", FallbackStepRef: "safe_path"})
	engine.RegisterWorkflow(&Workflow{
		WorkflowID:  "resilient",
		EntryStepID: "explode",
		Steps: map[string]Step{
			"explode": &LeafStep{
				StepID: "explode",
				Run: func(ctx map[string]any) (map[string]any, error) {
					return nil, errors.New("boom")
				},
				CircuitBreakerPolicyRef: "retry_elsewhere",
			},
			"safe_path": &LeafStep{
				StepID: "safe_path",
				Run: func(ctx map[string]any) (map[string]any, error) {
					return map[string]any{"handled": true}, nil
				},
			},
		},
	})

	result, err := engine.Run("resilient", map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Output["handled"] != true {
		t.Fatalf("expected handled=true, got %v", result.Output)
	}
	if len(result.Trace) != 2 || result.Trace[1] != "safe_path" {
		t.Fatalf("expected trace to fall through to safe_path, got %v", result.Trace)
	}
}

func TestOnSuccessCycleHitsTheStepLimit(t *testing.T) {
	engine := NewEngine(nil)
	engine.SetLimits(50, 0)
	noop := func(ctx map[string]any) (map[string]any, error) { return map[string]any{}, nil }
	engine.RegisterWorkflow(&Workflow{
		WorkflowID:  "cyclic",
		EntryStepID: "a",
		Steps: map[string]Step{
			"a": &LeafStep{StepID: "a", Run: noop, OnSuccess: "b"},
			"b": &LeafStep{StepID: "b", Run: noop, OnSuccess: "a"},
		},
	})

	_, err := engine.Run("cyclic", map[string]any{})

	var limit *StepLimitExceeded
	if !errors.As(err, &limit) {
		t.Fatalf("expected *StepLimitExceeded, got %v", err)
	}
	if limit.WorkflowID != "cyclic" || limit.Limit != 50 {
		t.Fatalf("unexpected limit detail: %+v", limit)
	}

	if len(limit.RecentSteps) == 0 {
		t.Fatalf("expected RecentSteps to point at the cycle, got none")
	}
}

func TestSubWorkflowCycleHitsTheDepthLimit(t *testing.T) {
	engine := NewEngine(nil)
	engine.SetLimits(0, 8)
	engine.RegisterWorkflow(&Workflow{
		WorkflowID:  "recursive",
		EntryStepID: "down",
		Steps: map[string]Step{
			"down": &CompositeStep{StepID: "down", SubWorkflowRef: "recursive"},
		},
	})

	_, err := engine.Run("recursive", map[string]any{})

	var limit *DepthLimitExceeded
	if !errors.As(err, &limit) {
		t.Fatalf("expected *DepthLimitExceeded, got %v", err)
	}
	if limit.Limit != 8 {
		t.Fatalf("unexpected limit detail: %+v", limit)
	}
}

func TestRunawayIsNotSwallowedByACircuitBreaker(t *testing.T) {
	engine := NewEngine(nil)
	engine.SetLimits(0, 4)
	engine.RegisterPolicy(CircuitBreakerPolicy{PolicyID: "catch_all", FallbackStepRef: "rescue"})
	engine.RegisterWorkflow(&Workflow{
		WorkflowID:  "recursive",
		EntryStepID: "down",
		Steps: map[string]Step{
			"down": &CompositeStep{
				StepID:                  "down",
				SubWorkflowRef:          "recursive",
				CircuitBreakerPolicyRef: "catch_all",
			},
			"rescue": &LeafStep{
				StepID: "rescue",
				Run: func(ctx map[string]any) (map[string]any, error) {
					return map[string]any{"rescued": true}, nil
				},
			},
		},
	})

	result, err := engine.Run("recursive", map[string]any{})

	var limit *DepthLimitExceeded
	if !errors.As(err, &limit) {
		t.Fatalf("expected the depth limit to bypass the breaker, got %v", err)
	}
	if result.Output["rescued"] == true {
		t.Fatalf("fallback step ran; a runaway must not be swallowed by a breaker")
	}
}

func TestSetLimitsIgnoresNonPositiveValues(t *testing.T) {
	engine := NewEngine(nil)
	engine.SetLimits(-1, 0)
	if engine.maxSteps != DefaultMaxSteps || engine.maxDepth != DefaultMaxDepth {
		t.Fatalf("ceilings must not be switchable off, got steps=%d depth=%d",
			engine.maxSteps, engine.maxDepth)
	}
}

type fakeSandbox struct {
	healthy     bool
	failureMode string
	lastRequest entities.ProbeRequest
}

func (f *fakeSandbox) Probe(req entities.ProbeRequest) (entities.ProbeResult, error) {
	f.lastRequest = req
	return entities.ProbeResult{Healthy: f.healthy, DetectedFailureMode: f.failureMode}, nil
}

func TestSandboxGateAllowsHealthyStep(t *testing.T) {
	engine := NewEngine(&fakeSandbox{healthy: true})
	engine.RegisterWorkflow(&Workflow{
		WorkflowID:  "gated",
		EntryStepID: "risky",
		Steps: map[string]Step{
			"risky": &LeafStep{
				StepID:          "risky",
				SandboxProbeRef: "check_impact",
				Run: func(ctx map[string]any) (map[string]any, error) {
					return map[string]any{"executed": true}, nil
				},
			},
		},
	})

	result, err := engine.Run("gated", map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Output["executed"] != true {
		t.Fatalf("expected step to execute after a healthy probe, got %v", result.Output)
	}
}

func TestProbeReceivesTheWorkflowsDomainNotItsRef(t *testing.T) {
	sb := &fakeSandbox{healthy: true}
	engine := NewEngine(sb)
	engine.RegisterWorkflow(&Workflow{
		WorkflowID:  "security.contain_threat",
		DomainID:    "security",
		EntryStepID: "risky",
		Steps: map[string]Step{
			"risky": &LeafStep{
				StepID:          "risky",
				SandboxProbeRef: "check_impact",
				Run: func(ctx map[string]any) (map[string]any, error) {
					return map[string]any{"executed": true}, nil
				},
			},
		},
	})

	if _, err := engine.Run("security.contain_threat", map[string]any{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sb.lastRequest.DomainID != "security" {
		t.Fatalf("probe should receive the domain %q, got %q", "security", sb.lastRequest.DomainID)
	}
}

func TestSandboxGateBlocksUnhealthyStepViaCircuitBreaker(t *testing.T) {
	engine := NewEngine(&fakeSandbox{healthy: false, failureMode: "would isolate a domain controller"})
	engine.RegisterPolicy(CircuitBreakerPolicy{PolicyID: "escalate", FallbackStepRef: "human_review"})
	engine.RegisterWorkflow(&Workflow{
		WorkflowID:  "gated",
		EntryStepID: "risky",
		Steps: map[string]Step{
			"risky": &LeafStep{
				StepID:                  "risky",
				SandboxProbeRef:         "check_impact",
				CircuitBreakerPolicyRef: "escalate",
				Run: func(ctx map[string]any) (map[string]any, error) {
					return map[string]any{"executed": true}, nil
				},
			},
			"human_review": &LeafStep{
				StepID: "human_review",
				Run: func(ctx map[string]any) (map[string]any, error) {
					return map[string]any{"escalated": true}, nil
				},
			},
		},
	})

	result, err := engine.Run("gated", map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Output["executed"] == true {
		t.Fatalf("expected the risky action to be skipped, not executed")
	}
	if result.Output["escalated"] != true {
		t.Fatalf("expected fallback to human_review, got %v", result.Output)
	}
}
