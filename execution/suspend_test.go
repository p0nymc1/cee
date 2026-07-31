package execution

import (
	"errors"
	"testing"
)

func approvalWorkflow() *Workflow {
	return &Workflow{
		WorkflowID:  "ops.contain",
		DomainID:    "ops",
		EntryStepID: "hold",
		Steps: map[string]Step{
			"hold": &LeafStep{
				StepID: "hold",
				Run: func(ctx map[string]any) (map[string]any, error) {
					return Suspend("awaiting human approval")
				},
				OnSuccess: "act",
			},
			"act": &LeafStep{
				StepID: "act",
				Run: func(ctx map[string]any) (map[string]any, error) {
					if ctx["approved"] != true {
						return map[string]any{"contained": false}, nil
					}
					return map[string]any{"contained": true}, nil
				},
			},
		},
	}
}

func newSuspendableEngine() (*Engine, *MemoryStore) {
	store := NewMemoryStore()
	engine := NewEngine(nil)
	engine.SetStore(store)
	engine.RegisterWorkflow(approvalWorkflow())
	return engine, store
}

func TestSuspendParksTheRunAndReturnsAPointer(t *testing.T) {
	engine, store := newSuspendableEngine()

	result, err := engine.Run("ops.contain", map[string]any{"host": "dc01"})
	if err != nil {
		t.Fatalf("suspending must not surface as an error: %v", err)
	}
	if result.StatePointer == "" {
		t.Fatal("expected a resume pointer")
	}

	if result.StatePointer == "ops.contain" {
		t.Fatal("StatePointer is still echoing the workflow ref")
	}

	pending := store.Pending()
	if len(pending) != 1 {
		t.Fatalf("expected one parked run, got %d", len(pending))
	}
	if pending[0].Reason != "awaiting human approval" {
		t.Fatalf("the reason should be visible to an operator, got %q", pending[0].Reason)
	}
	if pending[0].Ctx["host"] != "dc01" {
		t.Fatalf("the context at the suspension point must be preserved, got %v", pending[0].Ctx)
	}
}

func TestResumeContinuesAfterTheSuspensionPoint(t *testing.T) {
	engine, _ := newSuspendableEngine()

	suspendResult, err := engine.Run("ops.contain", map[string]any{"host": "dc01"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	result, err := engine.Resume(suspendResult.StatePointer, map[string]any{"approved": true})
	if err != nil {
		t.Fatalf("unexpected error resuming: %v", err)
	}
	if result.Output["contained"] != true {
		t.Fatalf("expected the run to complete after approval, got %v", result.Output)
	}

	if result.Output["host"] != "dc01" {
		t.Fatalf("pre-suspension context was lost, got %v", result.Output)
	}

	if len(result.Trace) != 2 || result.Trace[0] != "hold" || result.Trace[1] != "act" {
		t.Fatalf("expected trace [hold act], got %v", result.Trace)
	}
}

func TestResumeCarriesADenialThrough(t *testing.T) {
	engine, _ := newSuspendableEngine()

	suspendResult, _ := engine.Run("ops.contain", map[string]any{"host": "dc01"})

	result, err := engine.Resume(suspendResult.StatePointer, map[string]any{"approved": false})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Output["contained"] != false {
		t.Fatalf("expected the denial to be honoured, got %v", result.Output)
	}
}

func TestPointerIsSingleUse(t *testing.T) {
	engine, store := newSuspendableEngine()

	suspendResult, _ := engine.Run("ops.contain", map[string]any{})
	if _, err := engine.Resume(suspendResult.StatePointer, map[string]any{"approved": true}); err != nil {
		t.Fatalf("first resume should succeed: %v", err)
	}

	if _, err := engine.Resume(suspendResult.StatePointer, map[string]any{"approved": true}); err == nil {
		t.Fatal("expected the second resume to fail; a decision must not be replayable")
	}
	if len(store.Pending()) != 0 {
		t.Fatal("a resumed run must not stay parked")
	}
}

func TestSuspendWithoutAStoreFailsLoudly(t *testing.T) {
	engine := NewEngine(nil)
	engine.RegisterWorkflow(approvalWorkflow())

	_, err := engine.Run("ops.contain", map[string]any{})

	var unsupported *NoSuspensionSupport
	if !errors.As(err, &unsupported) {
		t.Fatalf("expected *NoSuspensionSupport, got %v", err)
	}
}

func TestSuspensionIsNotSwallowedByACircuitBreaker(t *testing.T) {
	store := NewMemoryStore()
	engine := NewEngine(nil)
	engine.SetStore(store)
	engine.RegisterPolicy(CircuitBreakerPolicy{PolicyID: "catch_all", FallbackStepRef: "fallback"})
	engine.RegisterWorkflow(&Workflow{
		WorkflowID:  "ops.gated",
		EntryStepID: "hold",
		Steps: map[string]Step{
			"hold": &LeafStep{
				StepID:                  "hold",
				CircuitBreakerPolicyRef: "catch_all",
				Run: func(ctx map[string]any) (map[string]any, error) {
					return Suspend("awaiting human approval")
				},
			},
			"fallback": &LeafStep{
				StepID: "fallback",
				Run: func(ctx map[string]any) (map[string]any, error) {
					return map[string]any{"fell_back": true}, nil
				},
			},
		},
	})

	result, err := engine.Run("ops.gated", map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Output["fell_back"] == true {
		t.Fatal("the breaker swallowed a suspension; it must only absorb failures")
	}
	if result.StatePointer == "" {
		t.Fatal("expected the run to park with a pointer")
	}
}

func TestNestedSuspensionIsRejected(t *testing.T) {
	store := NewMemoryStore()
	engine := NewEngine(nil)
	engine.SetStore(store)
	engine.RegisterWorkflow(&Workflow{
		WorkflowID:  "ops.outer",
		EntryStepID: "down",
		Steps: map[string]Step{
			"down": &CompositeStep{StepID: "down", SubWorkflowRef: "ops.inner"},
		},
	})
	engine.RegisterWorkflow(&Workflow{
		WorkflowID:  "ops.inner",
		EntryStepID: "hold",
		Steps: map[string]Step{
			"hold": &LeafStep{
				StepID: "hold",
				Run: func(ctx map[string]any) (map[string]any, error) {
					return Suspend("awaiting human approval")
				},
			},
		},
	})

	_, err := engine.Run("ops.outer", map[string]any{})

	var nested *NestedSuspensionUnsupported
	if !errors.As(err, &nested) {
		t.Fatalf("expected *NestedSuspensionUnsupported, got %v", err)
	}
	if len(store.Pending()) != 0 {
		t.Fatal("a rejected suspension must not leave a parked run behind")
	}
}

func TestResumeRejectsAnUnknownPointer(t *testing.T) {
	engine, _ := newSuspendableEngine()
	if _, err := engine.Resume("not-a-real-pointer", nil); err == nil {
		t.Fatal("expected an error for an unknown pointer")
	}
}

func TestPointersAreUnguessable(t *testing.T) {
	engine, _ := newSuspendableEngine()

	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		result, err := engine.Run("ops.contain", map[string]any{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if seen[result.StatePointer] {
			t.Fatalf("pointer %q was issued twice", result.StatePointer)
		}
		seen[result.StatePointer] = true
	}
}
