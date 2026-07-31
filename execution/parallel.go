package execution

import (
	"fmt"
	"reflect"
	"strings"
	"sync"

	"github.com/p0nymc1/cee/entities"
)

type ParallelStep struct {
	StepID                  string
	Branches                []string
	CircuitBreakerPolicyRef string
	OnSuccess               string
}

func (s *ParallelStep) ID() string                      { return s.StepID }
func (s *ParallelStep) circuitBreakerPolicyRef() string { return s.CircuitBreakerPolicyRef }

type NoBranches struct {
	WorkflowID string
	StepID     string
}

func (e *NoBranches) Error() string {
	return fmt.Sprintf(
		"step %q in workflow %q is parallel but declares no branches",
		e.StepID, e.WorkflowID,
	)
}

type ConflictingBranchWrites struct {
	StepID       string
	Field        string
	FirstBranch  string
	FirstValue   any
	SecondBranch string
	SecondValue  any
}

func (e *ConflictingBranchWrites) Error() string {
	return fmt.Sprintf(
		"parallel step %q cannot join: branches %q and %q both wrote %q, as %v and %v; "+
			"which one wins would depend on nothing the workflow states, so have them write different fields",
		e.StepID, e.FirstBranch, e.SecondBranch, e.Field, e.FirstValue, e.SecondValue,
	)
}

type BranchFailure struct {
	Branch string
	Reason string
}

func (f BranchFailure) String() string { return f.Branch + ": " + f.Reason }

type BranchesFailed struct {
	StepID   string
	Total    int
	Failures []BranchFailure
}

func (e *BranchesFailed) Error() string {
	parts := make([]string, 0, len(e.Failures))
	for _, f := range e.Failures {
		parts = append(parts, f.String())
	}
	return fmt.Sprintf("%d of %d branches of parallel step %q failed: %s",
		len(e.Failures), e.Total, e.StepID, strings.Join(parts, "; "))
}

type BranchPanicked struct {
	StepID string
	Branch string
	Value  any
}

func (e *BranchPanicked) Error() string {
	return fmt.Sprintf("branch %q of parallel step %q panicked: %v", e.Branch, e.StepID, e.Value)
}

type branchOutcome struct {
	result entities.WorkflowResult
	err    error
}

func (e *Engine) runParallel(
	workflow *Workflow, s *ParallelStep, ctx map[string]any, depth int,
) (map[string]any, []string, error) {
	if len(s.Branches) == 0 {
		return nil, nil, &NoBranches{WorkflowID: workflow.WorkflowID, StepID: s.StepID}
	}

	outcomes := make([]branchOutcome, len(s.Branches))
	var wg sync.WaitGroup
	for i, ref := range s.Branches {
		wg.Add(1)
		go func(i int, ref string) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					outcomes[i] = branchOutcome{
						err: &BranchPanicked{StepID: s.StepID, Branch: ref, Value: r},
					}
				}
			}()
			result, err := e.run(ref, cloneContext(ctx), depth+1)
			outcomes[i] = branchOutcome{result: result, err: err}
		}(i, ref)
	}
	wg.Wait()

	var failures []BranchFailure
	for i, outcome := range outcomes {
		if outcome.err == nil {
			continue
		}
		if bypassesBreaker(outcome.err) {
			return nil, nil, outcome.err
		}
		failures = append(failures, BranchFailure{Branch: s.Branches[i], Reason: outcome.err.Error()})
	}
	if len(failures) > 0 {
		return nil, nil, &BranchesFailed{StepID: s.StepID, Total: len(s.Branches), Failures: failures}
	}

	var trace []string
	joined := make(map[string]any)
	wroteBy := make(map[string]string)
	wroteValue := make(map[string]any)

	for i, outcome := range outcomes {
		branch := s.Branches[i]
		trace = append(trace, outcome.result.Trace...)

		for field, value := range branchDelta(ctx, outcome.result.Output) {
			if owner, taken := wroteBy[field]; taken && !sameValue(wroteValue[field], value) {
				return nil, nil, &ConflictingBranchWrites{
					StepID:       s.StepID,
					Field:        field,
					FirstBranch:  owner,
					FirstValue:   wroteValue[field],
					SecondBranch: branch,
					SecondValue:  value,
				}
			}
			joined[field] = value
			wroteBy[field] = branch
			wroteValue[field] = value
		}
	}

	return merge(ctx, joined), trace, nil
}

func branchDelta(before, after map[string]any) map[string]any {
	out := make(map[string]any)
	for field, value := range after {
		previous, existed := before[field]
		if !existed || !sameValue(previous, value) {
			out[field] = value
		}
	}
	return out
}

func cloneContext(ctx map[string]any) map[string]any {
	out := make(map[string]any, len(ctx))
	for k, v := range ctx {
		out[k] = v
	}
	return out
}

func sameValue(a, b any) bool { return reflect.DeepEqual(a, b) }
