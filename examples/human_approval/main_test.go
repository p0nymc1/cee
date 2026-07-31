package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMain(m *testing.M) {
	if err := os.Chdir(filepath.Join("..", "..")); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}

func TestUnderThresholdIsAutoApproved(t *testing.T) {
	_, engine, store, err := buildRuntime()
	if err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	result, err := engine.Run("expense-approval.review", map[string]any{"amount": 240.0})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Output["outcome"] != "auto-approved" {
		t.Fatalf("expected auto-approval, got %v", result.Output)
	}
	if len(store.Pending()) != 0 {
		t.Fatal("a claim under the threshold must not park for a human")
	}
}

func TestOverThresholdParksThenResumes(t *testing.T) {
	_, engine, store, err := buildRuntime()
	if err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	suspended, err := engine.Run("expense-approval.review",
		map[string]any{"amount": 4800.0, "claimant": "wei"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if suspended.StatePointer == "" {
		t.Fatal("expected the run to park with a resume pointer")
	}
	if len(store.Pending()) != 1 {
		t.Fatalf("expected one parked run, got %d", len(store.Pending()))
	}

	resumed, err := engine.Resume(suspended.StatePointer, map[string]any{"approved": true})
	if err != nil {
		t.Fatalf("unexpected error resuming: %v", err)
	}
	if resumed.Output["outcome"] != "approved by manager" {
		t.Fatalf("expected manager approval to land, got %v", resumed.Output)
	}

	if resumed.Output["claimant"] != "wei" {
		t.Fatalf("pre-suspension context was lost, got %v", resumed.Output)
	}

	want := []string{"check_threshold", "hold_for_human", "apply_decision", "record_approved"}
	if len(resumed.Trace) != len(want) {
		t.Fatalf("expected trace %v, got %v", want, resumed.Trace)
	}
	for i, step := range want {
		if resumed.Trace[i] != step {
			t.Fatalf("expected trace %v, got %v", want, resumed.Trace)
		}
	}
}

func TestManagerRejectionIsRecorded(t *testing.T) {
	_, engine, _, err := buildRuntime()
	if err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	suspended, _ := engine.Run("expense-approval.review", map[string]any{"amount": 4800.0})

	resumed, err := engine.Resume(suspended.StatePointer, map[string]any{"approved": false})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resumed.Output["outcome"] != "rejected by manager" {
		t.Fatalf("expected the rejection to be recorded, got %v", resumed.Output)
	}
}
