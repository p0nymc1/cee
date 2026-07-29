package filestore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cee/execution"
)

func newStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	store, err := New(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return store, dir
}

func sampleState(pointer string) execution.State {
	return execution.State{
		Pointer:    pointer,
		WorkflowID: "expense-approval.review",
		StepID:     "hold_for_human",
		Reason:     "awaiting manager decision",
		Ctx:        map[string]any{"amount": 4800.0, "claimant": "wei"},
		Trace:      []string{"check_threshold", "hold_for_human"},
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	store, _ := newStore(t)
	want := sampleState("abc123")

	if err := store.Save(want); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, err := store.Load("abc123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got.WorkflowID != want.WorkflowID || got.StepID != want.StepID || got.Reason != want.Reason {
		t.Fatalf("state did not survive the round trip: %+v", got)
	}
	if got.Ctx["claimant"] != "wei" || got.Ctx["amount"] != 4800.0 {
		t.Fatalf("context did not survive the round trip: %v", got.Ctx)
	}
	if len(got.Trace) != 2 || got.Trace[0] != "check_threshold" {
		t.Fatalf("trace did not survive the round trip: %v", got.Trace)
	}
}

// The whole point of this Store: a second process, or the same process after
// a restart, must find the parked run.
func TestStateSurvivesANewStoreOverTheSameDirectory(t *testing.T) {
	store, dir := newStore(t)
	if err := store.Save(sampleState("abc123")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	restarted, err := New(dir) // stands in for a process restart
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, err := restarted.Load("abc123")
	if err != nil {
		t.Fatalf("a parked run must survive a restart: %v", err)
	}
	if got.Reason != "awaiting manager decision" {
		t.Fatalf("unexpected state after restart: %+v", got)
	}
}

// End to end against the real engine: park a run, throw the engine away,
// build a new one over the same directory, and resume there.
func TestResumeWorksAcrossAnEngineRestart(t *testing.T) {
	_, dir := newStore(t)

	workflow := func() *execution.Workflow {
		return &execution.Workflow{
			WorkflowID:  "ops.approve",
			EntryStepID: "hold",
			Steps: map[string]execution.Step{
				"hold": &execution.LeafStep{
					StepID: "hold",
					Run: func(ctx map[string]any) (map[string]any, error) {
						return execution.Suspend("awaiting human approval")
					},
					OnSuccess: "act",
				},
				"act": &execution.LeafStep{
					StepID: "act",
					Run: func(ctx map[string]any) (map[string]any, error) {
						return map[string]any{"done": ctx["approved"]}, nil
					},
				},
			},
		}
	}

	before, err := New(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	engineBefore := execution.NewEngine(nil)
	engineBefore.SetStore(before)
	engineBefore.RegisterWorkflow(workflow())

	parked, err := engineBefore.Run("ops.approve", map[string]any{"claimant": "wei"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if parked.StatePointer == "" {
		t.Fatal("expected a resume pointer")
	}

	// Everything above is now gone; only the directory remains.
	after, err := New(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	engineAfter := execution.NewEngine(nil)
	engineAfter.SetStore(after)
	engineAfter.RegisterWorkflow(workflow())

	result, err := engineAfter.Resume(parked.StatePointer, map[string]any{"approved": true})
	if err != nil {
		t.Fatalf("resume after restart failed: %v", err)
	}
	if result.Output["done"] != true {
		t.Fatalf("expected the resumed run to finish, got %v", result.Output)
	}
	if result.Output["claimant"] != "wei" {
		t.Fatalf("pre-suspension context was lost across the restart: %v", result.Output)
	}
}

func TestDeleteConsumesThePointer(t *testing.T) {
	store, _ := newStore(t)
	if err := store.Save(sampleState("abc123")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := store.Delete("abc123"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := store.Load("abc123"); err == nil {
		t.Fatal("expected the pointer to be gone")
	}
	// A double delete must be reported, not silently tolerated: the engine
	// relies on Delete to guarantee an approval cannot be replayed.
	if err := store.Delete("abc123"); err == nil {
		t.Fatal("expected deleting a consumed pointer to fail")
	}
}

func TestLoadUnknownPointerFails(t *testing.T) {
	store, _ := newStore(t)
	if _, err := store.Load("nosuchpointer"); err == nil {
		t.Fatal("expected an error for an unknown pointer")
	}
}

// A pointer arrives from outside -- a CLI argument, an HTTP parameter -- and
// is used as a filename, so it must not be able to address anything outside
// the store's directory.
func TestPointerCannotEscapeTheDirectory(t *testing.T) {
	store, dir := newStore(t)

	secret := filepath.Join(filepath.Dir(dir), "secret.txt")
	if err := os.WriteFile(secret, []byte("not yours"), 0o600); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, pointer := range []string{
		"../secret.txt",
		"../../etc/passwd",
		"/etc/passwd",
		"foo/bar",
		"..",
		"",
		"has space",
		"nul\x00byte",
	} {
		if _, err := store.Load(pointer); err == nil {
			t.Fatalf("Load accepted an unsafe pointer %q", pointer)
		}
		if err := store.Delete(pointer); err == nil {
			t.Fatalf("Delete accepted an unsafe pointer %q", pointer)
		}
		if err := store.Save(execution.State{Pointer: pointer}); err == nil {
			t.Fatalf("Save accepted an unsafe pointer %q", pointer)
		}
	}

	// The bystander file must still be there and untouched.
	if data, err := os.ReadFile(secret); err != nil || string(data) != "not yours" {
		t.Fatalf("a neighbouring file was disturbed: %v %q", err, data)
	}
}

func TestPendingListsParkedRuns(t *testing.T) {
	store, dir := newStore(t)
	if err := store.Save(sampleState("aaa")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := store.Save(sampleState("bbb")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// One corrupt file must not hide the healthy ones from an operator.
	if err := os.WriteFile(filepath.Join(dir, "ccc"), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	pending, err := store.Pending()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pending) != 2 {
		t.Fatalf("expected 2 readable parked runs, got %d", len(pending))
	}
	for _, state := range pending {
		if state.Reason != "awaiting manager decision" {
			t.Fatalf("unexpected reason: %q", state.Reason)
		}
	}
}

func TestCorruptStateIsReportedNotGuessedAt(t *testing.T) {
	store, dir := newStore(t)
	if err := os.WriteFile(filepath.Join(dir, "abc123"), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, err := store.Load("abc123")
	if err == nil {
		t.Fatal("expected corrupt state to be reported")
	}
	if !strings.Contains(err.Error(), "corrupt") {
		t.Fatalf("expected the error to say the state is corrupt, got %v", err)
	}
}

// Documented behaviour, pinned so it cannot change unnoticed: JSON decodes
// every number as float64, so an int in context is a float64 after a resume.
func TestNumbersComeBackAsFloat64(t *testing.T) {
	store, _ := newStore(t)
	state := sampleState("abc123")
	state.Ctx = map[string]any{"count": 42} // an int going in

	if err := store.Save(state); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, err := store.Load("abc123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, isInt := got.Ctx["count"].(int); isInt {
		t.Fatal("an int surviving as an int would contradict the documented behaviour")
	}
	if got.Ctx["count"].(float64) != 42 {
		t.Fatalf("expected 42 as a float64, got %v", got.Ctx["count"])
	}
}

func TestNewRejectsAnEmptyDirectory(t *testing.T) {
	if _, err := New(""); err == nil {
		t.Fatal("expected an error for an empty directory")
	}
}

func TestSavedFilesAreOwnerOnly(t *testing.T) {
	store, dir := newStore(t)
	if err := store.Save(sampleState("abc123")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Parked state holds business context, so it must not be world-readable.
	info, err := os.Stat(filepath.Join(dir, "abc123"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("expected 0600, got %#o", perm)
	}
}
