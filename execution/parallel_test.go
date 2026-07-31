package execution

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func branchWorkflow(id, stepID string, run Action) *Workflow {
	return &Workflow{
		WorkflowID:  id,
		EntryStepID: stepID,
		Steps:       map[string]Step{stepID: &LeafStep{StepID: stepID, Run: run}},
	}
}

func writes(field string, value any) Action {
	return func(ctx map[string]any) (map[string]any, error) {
		return map[string]any{field: value}, nil
	}
}

func slowWrites(field string, value any, d time.Duration) Action {
	return func(ctx map[string]any) (map[string]any, error) {
		time.Sleep(d)
		return map[string]any{field: value}, nil
	}
}

func parallelEngine(t *testing.T, branches ...*Workflow) *Engine {
	t.Helper()
	engine := NewEngine(nil)
	refs := make([]string, 0, len(branches))
	for _, b := range branches {
		engine.RegisterWorkflow(b)
		refs = append(refs, b.WorkflowID)
	}
	engine.RegisterWorkflow(&Workflow{
		WorkflowID:  "main",
		EntryStepID: "gather",
		Steps: map[string]Step{
			"gather": &ParallelStep{StepID: "gather", Branches: refs, OnSuccess: "decide"},
			"decide": &LeafStep{StepID: "decide", Run: func(ctx map[string]any) (map[string]any, error) {
				return map[string]any{"decided": true}, nil
			}},
		},
	})
	return engine
}

func TestParallelBranchesAllContributeToTheJoinedContext(t *testing.T) {
	engine := parallelEngine(t,
		branchWorkflow("credit", "check_credit", writes("credit_score", 780.0)),
		branchWorkflow("fraud", "check_fraud", writes("fraud_flag", false)),
		branchWorkflow("kyc", "check_kyc", writes("kyc_status", "clear")),
	)

	result, err := engine.Run("main", map[string]any{"applicant": "a-1"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for field, want := range map[string]any{
		"credit_score": 780.0, "fraud_flag": false, "kyc_status": "clear",
		"applicant": "a-1", "decided": true,
	} {
		if result.Output[field] != want {
			t.Errorf("output[%q] = %v, want %v", field, result.Output[field], want)
		}
	}
}

func TestParallelJoinIsDeterministicWhateverTheSchedulingOrder(t *testing.T) {
	engine := parallelEngine(t,
		branchWorkflow("slow", "slow_step", slowWrites("order", "slow", 20*time.Millisecond)),
		branchWorkflow("fast", "fast_step", writes("fast_done", true)),
	)

	first, err := engine.Run("main", map[string]any{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := strings.Join(first.Trace, " -> ")

	for i := 0; i < 20; i++ {
		result, err := engine.Run("main", map[string]any{})
		if err != nil {
			t.Fatalf("Run %d: %v", i, err)
		}
		if got := strings.Join(result.Trace, " -> "); got != want {
			t.Fatalf("run %d produced trace %q, want %q; the join must not depend on which branch finishes first",
				i, got, want)
		}
	}

	if want != "gather -> slow_step -> fast_step -> decide" {
		t.Errorf("trace = %q; branches must appear in declaration order, not completion order", want)
	}
}

func TestParallelBranchesRunConcurrentlyRatherThanInSequence(t *testing.T) {
	const pause = 60 * time.Millisecond
	engine := parallelEngine(t,
		branchWorkflow("a", "a_step", slowWrites("a", 1, pause)),
		branchWorkflow("b", "b_step", slowWrites("b", 2, pause)),
		branchWorkflow("c", "c_step", slowWrites("c", 3, pause)),
	)

	start := time.Now()
	if _, err := engine.Run("main", map[string]any{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	elapsed := time.Since(start)

	if elapsed > 2*pause {
		t.Errorf("three %v branches took %v; they ran in sequence, not in parallel", pause, elapsed)
	}
}

func TestParallelBranchesCannotSeeEachOthersWrites(t *testing.T) {
	peek := branchWorkflow("peek", "peek_step", func(ctx map[string]any) (map[string]any, error) {
		if _, leaked := ctx["written_by_other"]; leaked {
			return nil, errors.New("a branch saw another branch's write")
		}
		return map[string]any{"peeked": true}, nil
	})
	engine := parallelEngine(t,
		branchWorkflow("writer", "write_step", writes("written_by_other", true)),
		peek,
	)

	if _, err := engine.Run("main", map[string]any{}); err != nil {
		t.Fatalf("branches must be isolated from each other: %v", err)
	}
}

func TestTwoBranchesWritingOneFieldDifferentlyIsRefused(t *testing.T) {
	engine := parallelEngine(t,
		branchWorkflow("a", "a_step", writes("verdict", "approve")),
		branchWorkflow("b", "b_step", writes("verdict", "decline")),
	)

	_, err := engine.Run("main", map[string]any{})
	var conflict *ConflictingBranchWrites
	if !errors.As(err, &conflict) {
		t.Fatalf("want ConflictingBranchWrites, got %v", err)
	}
	if conflict.Field != "verdict" {
		t.Errorf("Field = %q, want %q", conflict.Field, "verdict")
	}
	if conflict.FirstBranch != "a" || conflict.SecondBranch != "b" {
		t.Errorf("conflict names branches %q and %q, want %q and %q",
			conflict.FirstBranch, conflict.SecondBranch, "a", "b")
	}
}

func TestTwoBranchesWritingOneFieldIdenticallyIsFine(t *testing.T) {
	engine := parallelEngine(t,
		branchWorkflow("a", "a_step", writes("region", "eu")),
		branchWorkflow("b", "b_step", writes("region", "eu")),
	)

	result, err := engine.Run("main", map[string]any{})
	if err != nil {
		t.Fatalf("agreeing branches must not conflict: %v", err)
	}
	if result.Output["region"] != "eu" {
		t.Errorf("region = %v, want eu", result.Output["region"])
	}
}

func TestABranchLeavingAnInheritedFieldAloneIsNotAConflict(t *testing.T) {
	engine := parallelEngine(t,
		branchWorkflow("a", "a_step", writes("status", "changed")),
		branchWorkflow("b", "b_step", writes("unrelated", true)),
	)

	result, err := engine.Run("main", map[string]any{"status": "original"})
	if err != nil {
		t.Fatalf("only one branch changed status, so there is no conflict: %v", err)
	}
	if result.Output["status"] != "changed" {
		t.Errorf("status = %v, want changed", result.Output["status"])
	}
}

func TestAConflictIsNotSwallowedByACircuitBreaker(t *testing.T) {
	engine := parallelEngine(t,
		branchWorkflow("a", "a_step", writes("verdict", "approve")),
		branchWorkflow("b", "b_step", writes("verdict", "decline")),
	)
	engine.RegisterPolicy(CircuitBreakerPolicy{PolicyID: "catch_all", FallbackStepRef: "decide"})
	main, _ := engine.workflow("main")
	main.Steps["gather"].(*ParallelStep).CircuitBreakerPolicyRef = "catch_all"

	_, err := engine.Run("main", map[string]any{})
	var conflict *ConflictingBranchWrites
	if !errors.As(err, &conflict) {
		t.Fatalf("a join conflict is a defect in the workflow's shape and must not be absorbed by a fallback; got %v", err)
	}
}

func TestEveryFailedBranchIsReportedNotJustTheFirst(t *testing.T) {
	fails := func(msg string) Action {
		return func(ctx map[string]any) (map[string]any, error) { return nil, errors.New(msg) }
	}
	engine := parallelEngine(t,
		branchWorkflow("a", "a_step", fails("credit bureau unreachable")),
		branchWorkflow("b", "b_step", writes("ok", true)),
		branchWorkflow("c", "c_step", fails("sanctions list stale")),
	)

	_, err := engine.Run("main", map[string]any{})
	if err == nil {
		t.Fatal("two failing branches must fail the parallel step")
	}
	if !strings.Contains(err.Error(), "2 of 3 branches") {
		t.Errorf("error %q does not say how many branches failed", err)
	}
	first := strings.Index(err.Error(), "credit bureau unreachable")
	second := strings.Index(err.Error(), "sanctions list stale")
	if first < 0 || second < 0 {
		t.Fatalf("error %q must mention every failure, not just the first", err)
	}
	if first > second {
		t.Errorf("error %q lists failures in completion order; they must be in declaration order", err)
	}
}

func TestBranchesFailedNamesEveryBranchInDeclarationOrder(t *testing.T) {
	fails := func(msg string) Action {
		return func(ctx map[string]any) (map[string]any, error) { return nil, errors.New(msg) }
	}
	engine := parallelEngine(t,
		branchWorkflow("a", "a_step", fails("bureau down")),
		branchWorkflow("b", "b_step", writes("ok", true)),
		branchWorkflow("c", "c_step", fails("list stale")),
	)
	main, _ := engine.workflow("main")
	step := main.Steps["gather"].(*ParallelStep)

	_, _, err := engine.runParallel(main, step, map[string]any{}, 0)
	var failed *BranchesFailed
	if !errors.As(err, &failed) {
		t.Fatalf("want BranchesFailed, got %v", err)
	}
	if len(failed.Failures) != 2 || failed.Total != 3 {
		t.Fatalf("reported %d of %d failures, want 2 of 3", len(failed.Failures), failed.Total)
	}
	if failed.Failures[0].Branch != "a" || failed.Failures[1].Branch != "c" {
		t.Errorf("failures listed as %v; they must be in declaration order", failed.Failures)
	}
}

func TestAFailedBranchCanBeCaughtByACircuitBreaker(t *testing.T) {
	engine := parallelEngine(t,
		branchWorkflow("a", "a_step", func(ctx map[string]any) (map[string]any, error) {
			return nil, errors.New("bureau down")
		}),
		branchWorkflow("b", "b_step", writes("ok", true)),
	)
	engine.RegisterPolicy(CircuitBreakerPolicy{PolicyID: "manual", FallbackStepRef: "manual_review"})
	main, _ := engine.workflow("main")
	main.Steps["gather"].(*ParallelStep).CircuitBreakerPolicyRef = "manual"
	main.Steps["manual_review"] = &LeafStep{StepID: "manual_review", Run: writes("routed", "human")}

	result, err := engine.Run("main", map[string]any{})
	if err != nil {
		t.Fatalf("a business failure in a branch should reach the fallback: %v", err)
	}
	if result.Output["routed"] != "human" {
		t.Errorf("routed = %v, want human", result.Output["routed"])
	}
	if !strings.Contains(fmt.Sprint(result.Output[FailureReasonKey]), "bureau down") {
		t.Errorf("the fallback was told %q; it needs to know which branch failed and why",
			result.Output[FailureReasonKey])
	}
}

func TestAPanickingBranchBecomesAnErrorRatherThanKillingTheProcess(t *testing.T) {
	engine := parallelEngine(t,
		branchWorkflow("a", "a_step", func(ctx map[string]any) (map[string]any, error) {
			return map[string]any{"n": ctx["missing"].(int)}, nil
		}),
		branchWorkflow("b", "b_step", writes("ok", true)),
	)

	_, err := engine.Run("main", map[string]any{})
	var panicked *BranchPanicked
	if !errors.As(err, &panicked) {
		t.Fatalf("want BranchPanicked, got %v", err)
	}
	if panicked.Branch != "a" {
		t.Errorf("Branch = %q, want %q", panicked.Branch, "a")
	}
}

func TestAParallelStepWithNoBranchesIsRefused(t *testing.T) {
	engine := NewEngine(nil)
	engine.RegisterWorkflow(&Workflow{
		WorkflowID:  "main",
		EntryStepID: "gather",
		Steps: map[string]Step{
			"gather": &ParallelStep{StepID: "gather"},
		},
	})

	_, err := engine.Run("main", map[string]any{})
	var none *NoBranches
	if !errors.As(err, &none) {
		t.Fatalf("want NoBranches, got %v", err)
	}
}

func TestABranchThatSuspendsIsRefusedRatherThanHalfResumed(t *testing.T) {
	engine := parallelEngine(t,
		branchWorkflow("a", "a_step", func(ctx map[string]any) (map[string]any, error) {
			return Suspend("waiting for a manager")
		}),
		branchWorkflow("b", "b_step", writes("ok", true)),
	)
	engine.SetStore(NewMemoryStore())

	_, err := engine.Run("main", map[string]any{})
	var nested *NestedSuspensionUnsupported
	if !errors.As(err, &nested) {
		t.Fatalf("want NestedSuspensionUnsupported, got %v", err)
	}
}

func TestARunawayBranchIsNotSwallowedByTheJoin(t *testing.T) {
	engine := NewEngine(nil)
	engine.RegisterWorkflow(&Workflow{
		WorkflowID:  "looping",
		EntryStepID: "spin",
		Steps: map[string]Step{
			"spin": &LeafStep{StepID: "spin", Run: writes("x", 1), OnSuccess: "spin"},
		},
	})
	engine.RegisterWorkflow(&Workflow{
		WorkflowID:  "main",
		EntryStepID: "gather",
		Steps: map[string]Step{
			"gather": &ParallelStep{
				StepID: "gather", Branches: []string{"looping"},
				CircuitBreakerPolicyRef: "catch_all", OnSuccess: "done",
			},
			"done": &LeafStep{StepID: "done", Run: writes("done", true)},
		},
	})
	engine.RegisterPolicy(CircuitBreakerPolicy{PolicyID: "catch_all", FallbackStepRef: "done"})
	engine.SetLimits(50, 8)

	_, err := engine.Run("main", map[string]any{})
	var limit *StepLimitExceeded
	if !errors.As(err, &limit) {
		t.Fatalf("a runaway branch is a structural defect and must bypass the breaker; got %v", err)
	}
}
