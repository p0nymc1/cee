package filestore

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/p0nymc1/cee/execution"
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

func TestStateSurvivesANewStoreOverTheSameDirectory(t *testing.T) {
	store, dir := newStore(t)
	if err := store.Save(sampleState("abc123")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	restarted, err := New(dir)
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

func TestNumbersComeBackAsFloat64(t *testing.T) {
	store, _ := newStore(t)
	state := sampleState("abc123")
	state.Ctx = map[string]any{"count": 42}

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

	info, err := os.Stat(filepath.Join(dir, "abc123"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("expected 0600, got %#o", perm)
	}
}

func TestConcurrentConsumeHasExactlyOneWinner(t *testing.T) {
	store, _ := newStore(t)
	if err := store.Save(sampleState("abc123")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	const racers = 32
	var start sync.WaitGroup
	var done sync.WaitGroup
	start.Add(1)
	done.Add(racers)

	var mu sync.Mutex
	var winners, losers int

	for i := 0; i < racers; i++ {
		go func() {
			defer done.Done()
			start.Wait()
			state, err := store.Consume("abc123")
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				winners++
				if state.Reason != "awaiting manager decision" {
					t.Errorf("winner got the wrong state: %+v", state)
				}
			} else {
				losers++
			}
		}()
	}

	start.Done()
	done.Wait()

	if winners != 1 {
		t.Fatalf("expected exactly 1 winner, got %d (losers %d)", winners, losers)
	}
	if losers != racers-1 {
		t.Fatalf("expected %d losers, got %d", racers-1, losers)
	}
}

func TestConcurrentResumeAppliesTheDecisionOnce(t *testing.T) {
	_, dir := newStore(t)
	store, err := New(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var applied int64
	engine := execution.NewEngine(nil)
	engine.SetStore(store)
	engine.RegisterWorkflow(&execution.Workflow{
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
					atomic.AddInt64(&applied, 1)
					return map[string]any{"done": true}, nil
				},
			},
		},
	})

	parked, err := engine.Run("ops.approve", map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	const racers = 16
	var start sync.WaitGroup
	var done sync.WaitGroup
	start.Add(1)
	done.Add(racers)
	for i := 0; i < racers; i++ {
		go func() {
			defer done.Done()
			start.Wait()
			engine.Resume(parked.StatePointer, map[string]any{"approved": true})
		}()
	}
	start.Done()
	done.Wait()

	if got := atomic.LoadInt64(&applied); got != 1 {
		t.Fatalf("the decision was applied %d times; it must be applied exactly once", got)
	}
}

func TestClaimIsHeldUntilReleased(t *testing.T) {
	store, dir := newStore(t)
	if err := store.Save(sampleState("abc123")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := store.Consume("abc123"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	pending, err := store.Pending()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("a claimed run must not still read as pending, got %d", len(pending))
	}

	claims, _ := filepath.Glob(filepath.Join(dir, "*.claimed"))
	if len(claims) != 1 {
		t.Fatalf("expected the claim to be held on disk, found %d", len(claims))
	}

	if err := store.Release("abc123"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	claims, _ = filepath.Glob(filepath.Join(dir, "*.claimed"))
	if len(claims) != 0 {
		t.Fatalf("Release must discard the claim, %d left", len(claims))
	}
}

func TestReleaseOfAnUnclaimedPointerIsNotAnError(t *testing.T) {
	store, _ := newStore(t)
	if err := store.Release("neverclaimed"); err != nil {
		t.Fatalf("expected a no-op, got %v", err)
	}
}

func TestOrphanedReportsAClaimNeverReleased(t *testing.T) {
	store, _ := newStore(t)
	if err := store.Save(sampleState("abc123")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := store.Consume("abc123"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	fresh, err := store.Orphaned(time.Hour)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fresh) != 0 {
		t.Fatalf("an in-flight run must not be reported as an orphan, got %d", len(fresh))
	}

	orphans, err := store.Orphaned(0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(orphans) != 1 {
		t.Fatalf("expected 1 orphan, got %d", len(orphans))
	}
	if orphans[0].Pointer != "abc123" {
		t.Fatalf("the orphan must name its pointer, got %q", orphans[0].Pointer)
	}

	if orphans[0].State.Reason != "awaiting manager decision" {
		t.Fatalf("the orphan must carry its reason, got %q", orphans[0].State.Reason)
	}
	if orphans[0].ClaimedAt.IsZero() {
		t.Fatal("the orphan must carry when it was claimed")
	}
}

func TestReleasedRunIsNotAnOrphan(t *testing.T) {
	store, _ := newStore(t)
	if err := store.Save(sampleState("abc123")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := store.Consume("abc123"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := store.Release("abc123"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	orphans, err := store.Orphaned(0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(orphans) != 0 {
		t.Fatalf("a completed run must leave no orphan, got %d", len(orphans))
	}
}

func TestEngineReleasesAfterAResume(t *testing.T) {
	_, dir := newStore(t)
	store, err := New(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	engine := execution.NewEngine(nil)
	engine.SetStore(store)
	engine.RegisterWorkflow(&execution.Workflow{
		WorkflowID:  "ops.approve",
		EntryStepID: "hold",
		Steps: map[string]execution.Step{
			"hold": &execution.LeafStep{
				StepID:    "hold",
				OnSuccess: "act",
				Run: func(ctx map[string]any) (map[string]any, error) {
					return execution.Suspend("awaiting human approval")
				},
			},
			"act": &execution.LeafStep{
				StepID: "act",
				Run: func(ctx map[string]any) (map[string]any, error) {
					return map[string]any{"done": true}, nil
				},
			},
		},
	})

	parked, err := engine.Run("ops.approve", map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := engine.Resume(parked.StatePointer, map[string]any{"approved": true}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	orphans, err := store.Orphaned(0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(orphans) != 0 {
		t.Fatalf("a resume that completed must release its claim, got %d orphans", len(orphans))
	}
}
