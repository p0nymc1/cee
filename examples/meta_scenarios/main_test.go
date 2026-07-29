package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/p0nymc1/cee/execution"
)

// The manifests are read by repo-relative path, as `go run ./examples/...`
// does.
func TestMain(m *testing.M) {
	if err := os.Chdir(filepath.Join("..", "..")); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}

func freshRuntime(t *testing.T) *runtime {
	t.Helper()
	// Each test gets its own target state, since the write action mutates it.
	targetVersions = map[string]float64{"row-1": 7, "row-2": 7, "row-3": 7}
	rt, err := buildRuntime()
	if err != nil {
		t.Fatalf("setup failed: %v", err)
	}
	return rt
}

// An N-way switch built from steps that have only two outbound edges each.
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

// Urgency wins over category: the first test in the chain short-circuits, so
// the chain's order encodes precedence.
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

// Scheduling with no clock in the engine: outside the window the run parks,
// and whatever does own a clock resumes it.
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

	// A stale read must be blocked by the probe, and the target must not move.
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

// The two ways the pre-write check can refuse reach the same fallback step,
// and must remain tellable apart once there.
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

// There is no loop in the DAG; a batch is the caller running the workflow once
// per record, so one bad record cannot affect another's outcome.
func TestBatchRecordsAreIndependent(t *testing.T) {
	rt := freshRuntime(t)

	batch := []map[string]any{
		{"record_id": "", "target_version_seen": 7.0},      // malformed
		{"record_id": "row-1", "target_version_seen": 7.0}, // clean, after a failure
		{"record_id": "row-9", "target_version_seen": 7.0}, // absent
		{"record_id": "row-3", "target_version_seen": 7.0}, // clean, after another
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
