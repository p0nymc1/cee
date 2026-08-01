package main

import "testing"

func TestOrdinaryHostIsAutoContained(t *testing.T) {
	router, engine := buildRuntime()

	match := router.Match("security", "repeated failed login attempts spike from one source")
	if !match.Matched {
		t.Fatalf("expected an ATT&CK technique match")
	}

	result, err := engine.Run(match.EntryWorkflowRef, map[string]any{"target_host": "ws-4471"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Output["contained"] != true {
		t.Fatalf("expected auto-containment, got %+v", result.Output)
	}
}

func TestCriticalAssetIsHeldForHumanApproval(t *testing.T) {
	router, engine := buildRuntime()

	match := router.Match("security", "repeated failed login attempts spike from one source")
	result, err := engine.Run(match.EntryWorkflowRef, map[string]any{"target_host": "dc01"})
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

func TestBatchDiagnosticsMeasuresTheErrorSide(t *testing.T) {
	r := runBatch()

	// One of five alerts (the database backup) matches no technique.
	if r.IntentMatches != 4 || r.IntentMisses != 1 {
		t.Errorf("intent: matches=%d misses=%d, want 4 and 1", r.IntentMatches, r.IntentMisses)
	}
	if got := r.IntentMissRate(); got != 0.2 {
		t.Errorf("intent miss rate = %v, want 0.2", got)
	}
	// Four events reach a probe; only dc01 refuses.
	if r.ProbesRun != 4 || r.ProbesRefused != 1 {
		t.Errorf("probe: run=%d refused=%d, want 4 and 1", r.ProbesRun, r.ProbesRefused)
	}
	if got := r.ProbeRefusalRate(); got != 0.25 {
		t.Errorf("probe refusal rate = %v, want 0.25", got)
	}
}
