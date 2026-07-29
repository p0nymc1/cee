package main

import "testing"

// TestOrdinaryHostIsAutoContained proves the deterministic happy path: a
// matched technique against a non-critical asset runs classify -> contain
// with no human in the loop.
func TestOrdinaryHostIsAutoContained(t *testing.T) {
	router, engine := buildRuntime()

	match := router.Match("security", "repeated failed login attempts spike from one source")
	if !match.Matched {
		t.Fatalf("expected an ATT&CK technique match")
	}

	result, err := engine.Run(match.EntryStepRef, map[string]any{"target_host": "ws-4471"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Output["contained"] != true {
		t.Fatalf("expected auto-containment, got %+v", result.Output)
	}
}

// TestCriticalAssetIsHeldForHumanApproval proves the containment-specific
// breaker semantics: the same technique against a domain controller is
// stopped by the sandbox gate and downgraded to human approval, never
// auto-executed.
func TestCriticalAssetIsHeldForHumanApproval(t *testing.T) {
	router, engine := buildRuntime()

	match := router.Match("security", "repeated failed login attempts spike from one source")
	result, err := engine.Run(match.EntryStepRef, map[string]any{"target_host": "dc01"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Output["contained"] == true {
		t.Fatalf("critical asset must NOT be auto-contained")
	}
	if result.Output["awaiting_human_approval"] != true {
		t.Fatalf("expected downgrade to human approval, got %+v", result.Output)
	}
}
