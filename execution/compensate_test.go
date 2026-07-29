package execution

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/p0nymc1/cee/entities"
)

// ledger stands in for the world: what a step actually did, and what an undo
// actually reversed.
type ledger struct{ events []string }

func (l *ledger) record(event string) Action {
	return func(ctx map[string]any) (map[string]any, error) {
		l.events = append(l.events, event)
		return map[string]any{}, nil
	}
}

func (l *ledger) fail(event string) Action {
	return func(ctx map[string]any) (map[string]any, error) {
		l.events = append(l.events, event)
		return nil, errors.New(event + " failed")
	}
}

// A three-step booking: charge the card, reserve the seat, then issue the
// ticket. Issuing fails, and the first two have already happened.
func booking(l *ledger, issue Action) *Workflow {
	return &Workflow{
		WorkflowID:  "travel.book",
		EntryStepID: "charge",
		Steps: map[string]Step{
			"charge": &LeafStep{
				StepID: "charge", Run: l.record("charged"),
				CompensateStepRef: "refund", OnSuccess: "reserve",
			},
			"reserve": &LeafStep{
				StepID: "reserve", Run: l.record("reserved"),
				CompensateStepRef: "release", OnSuccess: "issue",
			},
			"issue": &LeafStep{StepID: "issue", Run: issue},

			"refund":  &LeafStep{StepID: "refund", Run: l.record("refunded")},
			"release": &LeafStep{StepID: "release", Run: l.record("released")},
		},
	}
}

func TestAbandonedRunUnwindsInReverseOrder(t *testing.T) {
	l := &ledger{}
	engine := NewEngine(nil)
	engine.RegisterWorkflow(booking(l, l.fail("issue")))

	_, err := engine.Run("travel.book", map[string]any{})

	var tripped *CircuitBreakerTripped
	if !errors.As(err, &tripped) {
		t.Fatalf("expected the run to be abandoned, got %v", err)
	}
	// Reverse order matters: the seat is released before the card is refunded,
	// because the reservation was made against the charge.
	want := []string{"charged", "reserved", "issue", "released", "refunded"}
	if strings.Join(l.events, ",") != strings.Join(want, ",") {
		t.Fatalf("expected %v, got %v", want, l.events)
	}
	if len(tripped.Compensated) != 2 || tripped.Compensated[0] != "reserve" || tripped.Compensated[1] != "charge" {
		t.Fatalf("the error should report what was undone, got %v", tripped.Compensated)
	}
	if len(tripped.CompensationFailures) != 0 {
		t.Fatalf("nothing should have failed to undo, got %v", tripped.CompensationFailures)
	}
}

// A step with a fallback is saying "I have a plan B". Continuing into it is
// the intended handling, so the work behind it must not be torn down.
func TestAFallbackDoesNotTriggerAnUnwind(t *testing.T) {
	l := &ledger{}
	engine := NewEngine(nil)
	engine.RegisterPolicy(CircuitBreakerPolicy{PolicyID: "try_paper", FallbackStepRef: "paper_ticket"})

	wf := booking(l, l.fail("issue"))
	wf.Steps["issue"] = &LeafStep{
		StepID: "issue", Run: l.fail("issue"), CircuitBreakerPolicyRef: "try_paper",
	}
	wf.Steps["paper_ticket"] = &LeafStep{StepID: "paper_ticket", Run: l.record("paper issued")}
	engine.RegisterWorkflow(wf)

	if _, err := engine.Run("travel.book", map[string]any{}); err != nil {
		t.Fatalf("the fallback should have carried the run: %v", err)
	}
	for _, event := range l.events {
		if event == "refunded" || event == "released" {
			t.Fatalf("a diverted run must not unwind: %v", l.events)
		}
	}
}

// The worst case the engine can report: the action happened and reversing it
// also failed, so only a person can resolve it. It must be loud, not swallowed.
func TestAFailedCompensationIsReportedNotRetried(t *testing.T) {
	l := &ledger{}
	engine := NewEngine(nil)

	wf := booking(l, l.fail("issue"))
	wf.Steps["refund"] = &LeafStep{StepID: "refund", Run: l.fail("refund")}
	engine.RegisterWorkflow(wf)

	_, err := engine.Run("travel.book", map[string]any{})

	var tripped *CircuitBreakerTripped
	if !errors.As(err, &tripped) {
		t.Fatalf("expected a tripped breaker, got %v", err)
	}
	if len(tripped.CompensationFailures) != 1 {
		t.Fatalf("expected one failed compensation, got %v", tripped.CompensationFailures)
	}
	if tripped.CompensationFailures[0].StepID != "charge" {
		t.Fatalf("the failure should name the step that could not be undone, got %+v", tripped.CompensationFailures[0])
	}
	// The seat still came back, so a partial unwind is still worth doing.
	if len(tripped.Compensated) != 1 || tripped.Compensated[0] != "reserve" {
		t.Fatalf("the successful part of the unwind should be reported too, got %v", tripped.Compensated)
	}
	// It must be impossible to miss in the error text.
	if !strings.Contains(err.Error(), "COULD NOT UNDO") {
		t.Fatalf("an unreversed side effect must be prominent, got %q", err.Error())
	}
	// Once is once. A failed undo is never retried.
	refunds := 0
	for _, event := range l.events {
		if event == "refund" {
			refunds++
		}
	}
	if refunds != 1 {
		t.Fatalf("a failed compensation must not be retried, ran %d times", refunds)
	}
}

// A step that declares no compensation is reported as simply not undoable
// rather than silently skipped.
func TestStepsWithoutCompensationAreLeftAlone(t *testing.T) {
	l := &ledger{}
	engine := NewEngine(nil)
	engine.RegisterWorkflow(&Workflow{
		WorkflowID:  "w",
		EntryStepID: "notify",
		Steps: map[string]Step{
			// Sending an email cannot be unsent, and pretending otherwise
			// would be worse than admitting it.
			"notify": &LeafStep{StepID: "notify", Run: l.record("emailed"), OnSuccess: "charge"},
			"charge": &LeafStep{StepID: "charge", Run: l.fail("charge")},
		},
	})

	_, err := engine.Run("w", map[string]any{})

	var tripped *CircuitBreakerTripped
	if !errors.As(err, &tripped) {
		t.Fatalf("expected a tripped breaker, got %v", err)
	}
	if len(tripped.Compensated) != 0 || len(tripped.CompensationFailures) != 0 {
		t.Fatalf("nothing declared a compensation, got %+v", tripped)
	}
	if strings.Contains(err.Error(), "undone") {
		t.Fatalf("no unwind happened, so the error should not claim one: %q", err.Error())
	}
}

// A compensation pointing at a step that does not exist is a defect in the
// workflow, and has to surface as an unreversed side effect rather than as
// nothing at all.
func TestAMissingCompensationStepIsReportedAsAFailure(t *testing.T) {
	l := &ledger{}
	engine := NewEngine(nil)
	engine.RegisterWorkflow(&Workflow{
		WorkflowID:  "w",
		EntryStepID: "charge",
		Steps: map[string]Step{
			"charge": &LeafStep{
				StepID: "charge", Run: l.record("charged"),
				CompensateStepRef: "nowhere", OnSuccess: "boom",
			},
			"boom": &LeafStep{StepID: "boom", Run: l.fail("boom")},
		},
	})

	_, err := engine.Run("w", map[string]any{})

	var tripped *CircuitBreakerTripped
	if !errors.As(err, &tripped) {
		t.Fatalf("expected a tripped breaker, got %v", err)
	}
	if len(tripped.CompensationFailures) != 1 ||
		!strings.Contains(tripped.CompensationFailures[0].Reason, "no step") {
		t.Fatalf("a dangling compensation should be reported, got %+v", tripped.CompensationFailures)
	}
}

// A runaway DAG means the workflow's shape is wrong. Running its compensations
// would be acting on a description nobody should trust yet.
func TestStructuralFailuresDoNotUnwind(t *testing.T) {
	l := &ledger{}
	engine := NewEngine(nil)
	engine.SetLimits(6, 0)
	engine.RegisterWorkflow(&Workflow{
		WorkflowID:  "cyclic",
		EntryStepID: "a",
		Steps: map[string]Step{
			"a": &LeafStep{StepID: "a", Run: l.record("a"), CompensateStepRef: "undo", OnSuccess: "b"},
			"b": &LeafStep{StepID: "b", Run: l.record("b"), OnSuccess: "a"},
			"undo": &LeafStep{StepID: "undo", Run: func(ctx map[string]any) (map[string]any, error) {
				l.events = append(l.events, "UNDO RAN")
				return map[string]any{}, nil
			}},
		},
	})

	_, err := engine.Run("cyclic", map[string]any{})

	var limit *StepLimitExceeded
	if !errors.As(err, &limit) {
		t.Fatalf("expected the step limit, got %v", err)
	}
	for _, event := range l.events {
		if event == "UNDO RAN" {
			t.Fatal("a malformed DAG must not have its compensations run")
		}
	}
}

// A probe refusing is a failure like any other: if the run is abandoned, work
// already done still has to come back.
func TestAProbeRefusalAlsoUnwinds(t *testing.T) {
	l := &ledger{}
	engine := NewEngine(proberFn(func() (bool, string) { return false, "target is closed" }))
	engine.RegisterWorkflow(&Workflow{
		WorkflowID:  "w",
		EntryStepID: "charge",
		Steps: map[string]Step{
			"charge": &LeafStep{
				StepID: "charge", Run: l.record("charged"),
				CompensateStepRef: "refund", OnSuccess: "ship",
			},
			"ship": &LeafStep{
				StepID: "ship", SandboxProbeRef: "check",
				Run: l.record("shipped"),
			},
			"refund": &LeafStep{StepID: "refund", Run: l.record("refunded")},
		},
	})

	_, err := engine.Run("w", map[string]any{})
	if err == nil {
		t.Fatal("expected the run to be abandoned")
	}
	if fmt.Sprint(l.events) != "[charged refunded]" {
		t.Fatalf("the charge should have been refunded and nothing shipped, got %v", l.events)
	}
}

// proberFn is a one-line Prober for tests that only care about the verdict.
type proberFn func() (bool, string)

func (f proberFn) Probe(entities.ProbeRequest) (entities.ProbeResult, error) {
	healthy, mode := f()
	return entities.ProbeResult{Healthy: healthy, DetectedFailureMode: mode}, nil
}
