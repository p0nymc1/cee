package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/p0nymc1/cee/execution"
)

func TestMain(m *testing.M) {
	if err := os.Chdir(filepath.Join("..", "..")); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}

func freshRuntime(t *testing.T) *runtime {
	t.Helper()

	targetVersions = map[string]float64{"row-1": 7, "row-2": 7, "row-3": 7}
	rt, err := buildRuntime()
	if err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	return rt
}

func TestTicketRoutingReachesEveryQueue(t *testing.T) {
	rt := freshRuntime(t)

	for _, tc := range []struct {
		ticket map[string]any
		queue  string
	}{
		{map[string]any{"severity": "urgent", "category": "bug"}, "pager-duty"},
		{map[string]any{"severity": "normal", "category": "billing"}, "finance-ops"},
		{map[string]any{"severity": "normal", "category": "crash"}, "engineering"},
		{map[string]any{"severity": "normal", "category": "how-do-i"}, "general-support"},
	} {
		result, err := rt.engine.Run("ticket-routing.triage", tc.ticket)
		if err != nil {
			t.Fatalf("unexpected error for %v: %v", tc.ticket, err)
		}
		if result.Output["queue"] != tc.queue {
			t.Fatalf("%v routed to %v, want %v", tc.ticket, result.Output["queue"], tc.queue)
		}
	}
}

func TestTicketRoutingOrderEncodesPrecedence(t *testing.T) {
	rt := freshRuntime(t)

	result, err := rt.engine.Run("ticket-routing.triage",
		map[string]any{"severity": "urgent", "category": "billing"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Output["queue"] != "pager-duty" {
		t.Fatalf("urgent should outrank category, got %v", result.Output["queue"])
	}
	if len(result.Trace) != 2 {
		t.Fatalf("an urgent ticket should not be tested against later rules, trace %v", result.Trace)
	}
}

func TestChangeWindowDefersAndResumes(t *testing.T) {
	rt := freshRuntime(t)

	direct, err := rt.engine.Run("change-window.apply", map[string]any{"in_maintenance_window": true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if direct.Output["applied"] != true {
		t.Fatalf("inside the window the change should apply, got %v", direct.Output)
	}
	if direct.StatePointer != "" {
		t.Fatal("inside the window nothing should park")
	}

	deferred, err := rt.engine.Run("change-window.apply", map[string]any{"in_maintenance_window": false})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if deferred.Output["applied"] == true {
		t.Fatal("outside the window the change must not apply")
	}
	if deferred.StatePointer == "" {
		t.Fatal("outside the window the run should park with a pointer")
	}

	resumed, err := rt.engine.Resume(deferred.StatePointer, map[string]any{"in_maintenance_window": true})
	if err != nil {
		t.Fatalf("unexpected error resuming: %v", err)
	}
	if resumed.Output["applied"] != true {
		t.Fatalf("the change should apply once the window opens, got %v", resumed.Output)
	}
}

func TestRecordSyncWritesOnlyTheCleanRecord(t *testing.T) {
	rt := freshRuntime(t)

	clean, err := rt.engine.Run("record-sync.push",
		map[string]any{"record_id": "row-1", "target_version_seen": 7.0})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if clean.Output["synced"] != true {
		t.Fatalf("a clean record should sync, got %v", clean.Output)
	}
	if targetVersions["row-1"] != 8 {
		t.Fatalf("the target should have advanced, got v%v", targetVersions["row-1"])
	}

	stale, err := rt.engine.Run("record-sync.push",
		map[string]any{"record_id": "row-2", "target_version_seen": 3.0})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stale.Output["synced"] != false {
		t.Fatalf("a stale read must not sync, got %v", stale.Output)
	}
	if targetVersions["row-2"] != 7 {
		t.Fatalf("a blocked write must not touch the target, got v%v", targetVersions["row-2"])
	}
}

func TestRecordSyncDistinguishesItsTwoRefusals(t *testing.T) {
	rt := freshRuntime(t)

	stale, err := rt.engine.Run("record-sync.push",
		map[string]any{"record_id": "row-2", "target_version_seen": 3.0})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	missing, err := rt.engine.Run("record-sync.push",
		map[string]any{"record_id": "row-9", "target_version_seen": 7.0})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if stale.Output["outcome"] != missing.Output["outcome"] {
		t.Fatal("this test is only meaningful if both land on the same fallback step")
	}
	staleReason, _ := stale.Output[execution.FailureReasonKey].(string)
	missingReason, _ := missing.Output[execution.FailureReasonKey].(string)
	if staleReason == "" || missingReason == "" {
		t.Fatalf("both refusals should carry a reason, got %q and %q", staleReason, missingReason)
	}
	if staleReason == missingReason {
		t.Fatalf("a moved row and an absent row collapsed into one reason: %q", staleReason)
	}
}

func TestBatchRecordsAreIndependent(t *testing.T) {
	rt := freshRuntime(t)

	batch := []map[string]any{
		{"record_id": "", "target_version_seen": 7.0},
		{"record_id": "row-1", "target_version_seen": 7.0},
		{"record_id": "row-9", "target_version_seen": 7.0},
		{"record_id": "row-3", "target_version_seen": 7.0},
	}

	synced := 0
	for _, record := range batch {
		result, err := rt.engine.Run("record-sync.push", record)
		if err != nil {
			t.Fatalf("a bad record must not halt the batch: %v", err)
		}
		if result.Output["synced"] == true {
			synced++
		}
	}
	if synced != 2 {
		t.Fatalf("expected the two clean records to sync regardless of their neighbours, got %d", synced)
	}
}
