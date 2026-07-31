package execution

import (
	"errors"
	"fmt"
	"strings"
)

type CompensationFailure struct {
	StepID         string
	CompensateStep string
	Reason         string
}

func (f CompensationFailure) String() string {
	return fmt.Sprintf("%s (via %s): %s", f.StepID, f.CompensateStep, f.Reason)
}

type completed struct {
	stepID         string
	compensateStep string
}

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
