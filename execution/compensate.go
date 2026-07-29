package execution

import (
	"errors"
	"fmt"
	"strings"
)

// Compensation is how CEE answers the half of the safety story a sandbox
// probe cannot reach.
//
// A probe stops a step from doing something it should not. It says nothing
// about a run that gets three steps in -- money moved, a host isolated -- and
// then fails at the fourth. The breaker either diverts to a fallback or trips,
// and in both cases the side effects of the first three are still out there in
// the world.
//
// A leaf step may therefore name another step in its workflow that undoes it.
// When a run is abandoned -- a failure with no fallback to continue into --
// the engine walks back through the steps that actually completed and runs
// their compensations in reverse order, innermost first.
//
// Two boundaries are deliberate:
//
//   - Unwinding happens on abandonment, not on every failure. A step that
//     declares a fallback is saying "I have a plan B"; continuing into it is
//     the intended handling, and tearing down the work behind it would be
//     wrong.
//   - A compensation that itself fails is never retried, and never silently
//     dropped. It is collected and reported, because a side effect that could
//     not be undone is the single most important thing a human needs told.

// CompensationFailure is a step whose undo did not work. This is the worst
// case in the model: the original action happened, and the attempt to reverse
// it also failed, so the world is in a state nobody chose.
type CompensationFailure struct {
	StepID         string
	CompensateStep string
	Reason         string
}

func (f CompensationFailure) String() string {
	return fmt.Sprintf("%s (via %s): %s", f.StepID, f.CompensateStep, f.Reason)
}

// completed records a step that ran to completion and knows how to undo
// itself, kept in execution order so the unwind can reverse it.
type completed struct {
	stepID         string
	compensateStep string
}

// unwind runs the compensations for steps that completed, most recent first.
//
// Reverse order matters: a later step's effects often depend on an earlier
// step's, so undoing the earlier one first can leave the later compensation
// with nothing coherent to act on.
func (e *Engine) unwind(workflow *Workflow, done []completed, ctx map[string]any) ([]string, []CompensationFailure) {
	var undone []string
	var failures []CompensationFailure

	for i := len(done) - 1; i >= 0; i-- {
		entry := done[i]

		step, ok := workflow.Steps[entry.compensateStep]
		if !ok {
			failures = append(failures, CompensationFailure{
				StepID:         entry.stepID,
				CompensateStep: entry.compensateStep,
				Reason:         fmt.Sprintf("workflow %q has no step %q", workflow.WorkflowID, entry.compensateStep),
			})
			continue
		}
		leaf, ok := step.(*LeafStep)
		if !ok {
			failures = append(failures, CompensationFailure{
				StepID:         entry.stepID,
				CompensateStep: entry.compensateStep,
				Reason:         "a compensation must be a leaf step",
			})
			continue
		}

		// Compensations are not probe-gated and not breaker-routed. They run
		// while a run is already being abandoned; sending a failed undo back
		// through the same machinery that is unwinding could re-enter it.
		output, err := leaf.Run(ctx)
		if err != nil {
			failures = append(failures, CompensationFailure{
				StepID:         entry.stepID,
				CompensateStep: entry.compensateStep,
				Reason:         err.Error(),
			})
			continue
		}
		ctx = merge(ctx, output)
		undone = append(undone, entry.stepID)
	}
	return undone, failures
}

// compensationSummary renders what an unwind achieved, for the error a caller
// receives.
func compensationSummary(undone []string, failures []CompensationFailure) string {
	var parts []string
	if len(undone) > 0 {
		parts = append(parts, "undone: "+strings.Join(undone, ", "))
	}
	if len(failures) > 0 {
		var f []string
		for _, failure := range failures {
			f = append(f, failure.String())
		}
		parts = append(parts, "COULD NOT UNDO: "+strings.Join(f, "; "))
	}
	if len(parts) == 0 {
		return ""
	}
	return " [" + strings.Join(parts, " | ") + "]"
}

// abandon unwinds a run that is ending without a fallback to continue into,
// and attaches the report to the error the caller receives.
//
// Only *CircuitBreakerTripped is unwound. The structural errors -- a runaway
// DAG, a misconfigured suspension -- mean the workflow's shape is wrong, and
// running its compensations would be acting on a description nobody should
// trust yet.
func (e *Engine) abandon(workflow *Workflow, done []completed, ctx map[string]any, cause error) error {
	var tripped *CircuitBreakerTripped
	if !errors.As(cause, &tripped) || len(done) == 0 {
		return cause
	}
	undone, failures := e.unwind(workflow, done, ctx)
	tripped.Compensated = undone
	tripped.CompensationFailures = failures
	return tripped
}
