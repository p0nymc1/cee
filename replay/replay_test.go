package replay_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/p0nymc1/cee/entities"
	"github.com/p0nymc1/cee/execution"
	"github.com/p0nymc1/cee/replay"
)

// A refund desk: pay out under the limit, hold above it, and never pay an
// account the probe says is closed. Threshold and probe are both injectable so
// a test can change the rule, or change the world, and replay against it.
func desk(limit float64, accountOpen bool) (*execution.Engine, execution.Prober) {
	sandbox := proberFunc(func(req entities.ProbeRequest) (entities.ProbeResult, error) {
		if accountOpen {
			return entities.ProbeResult{Healthy: true}, nil
		}
		return entities.ProbeResult{Healthy: false, DetectedFailureMode: "account is closed"}, nil
	})

	build := func(p execution.Prober) *execution.Engine {
		engine := execution.NewEngine(p)
		engine.RegisterPolicy(execution.CircuitBreakerPolicy{PolicyID: "hold_it", FallbackStepRef: "hold"})
		engine.RegisterWorkflow(&execution.Workflow{
			WorkflowID:  "refunds.process",
			EntryStepID: "pay",
			Steps: map[string]execution.Step{
				"pay": &execution.LeafStep{
					StepID:                  "pay",
					SandboxProbeRef:         "refund.account_open",
					CircuitBreakerPolicyRef: "hold_it",
					Run: func(ctx map[string]any) (map[string]any, error) {
						amount, _ := ctx["amount"].(float64)
						if amount > limit {
							return nil, fmt.Errorf("refund of %.0f is over the %.0f limit", amount, limit)
						}
						return map[string]any{"paid": true}, nil
					},
				},
				"hold": &execution.LeafStep{
					StepID: "hold",
					Run: func(ctx map[string]any) (map[string]any, error) {
						return map[string]any{"paid": false}, nil
					},
				},
			},
		})
		return engine
	}
	return build(sandbox), sandbox
}

type proberFunc func(entities.ProbeRequest) (entities.ProbeResult, error)

func (f proberFunc) Probe(r entities.ProbeRequest) (entities.ProbeResult, error) { return f(r) }

// record runs once through a Recorder and returns the recording.
func record(t *testing.T, limit float64, accountOpen bool, input map[string]any) replay.Recording {
	t.Helper()
	_, sandbox := desk(limit, accountOpen)
	rec := replay.NewRecorder(sandbox)

	engine := execution.NewEngine(rec)
	engine.RegisterPolicy(execution.CircuitBreakerPolicy{PolicyID: "hold_it", FallbackStepRef: "hold"})
	engine.RegisterWorkflow(workflowFor(limit))

	result, err := engine.Run("refunds.process", input)
	return rec.Finish("refunds.process", input, result, err)
}

func workflowFor(limit float64) *execution.Workflow {
	return &execution.Workflow{
		WorkflowID:  "refunds.process",
		EntryStepID: "pay",
		Steps: map[string]execution.Step{
			"pay": &execution.LeafStep{
				StepID:                  "pay",
				SandboxProbeRef:         "refund.account_open",
				CircuitBreakerPolicyRef: "hold_it",
				Run: func(ctx map[string]any) (map[string]any, error) {
					amount, _ := ctx["amount"].(float64)
					if amount > limit {
						return nil, fmt.Errorf("refund of %.0f is over the %.0f limit", amount, limit)
					}
					return map[string]any{"paid": true}, nil
				},
			},
			"hold": &execution.LeafStep{
				StepID: "hold",
				Run: func(ctx map[string]any) (map[string]any, error) {
					return map[string]any{"paid": false}, nil
				},
			},
		},
	}
}

// replayAgainst re-runs a recording against a workflow built with a possibly
// different rule, answering probes from the record rather than the world.
func replayAgainst(rec replay.Recording, limit float64) (entities.WorkflowResult, error, []string) {
	player := replay.NewPlayer(rec)
	engine := execution.NewEngine(player)
	engine.RegisterPolicy(execution.CircuitBreakerPolicy{PolicyID: "hold_it", FallbackStepRef: "hold"})
	engine.RegisterWorkflow(workflowFor(limit))

	result, err := engine.Run(rec.WorkflowID, rec.Input)
	return result, err, player.Unmatched()
}

// The determinism claim, cashed: same recording, same rule, nothing moved.
func TestReplayingAnUnchangedRuleShowsNoDifference(t *testing.T) {
	rec := record(t, 100, true, map[string]any{"account": "acct-1", "amount": 20.0})

	result, err, unmatched := replayAgainst(rec, 100)
	if len(unmatched) != 0 {
		t.Fatalf("the replay went somewhere the recording did not cover: %v", unmatched)
	}
	if diffs := replay.Compare(rec, result, err); len(diffs) != 0 {
		t.Fatalf("a deterministic run replayed differently: %v", diffs)
	}
}

// The reason this package exists: change a threshold, replay history, and read
// off exactly which decisions flip. Nothing about the recorded run changes --
// only the rule.
func TestLoweringAThresholdFlipsARecordedDecision(t *testing.T) {
	// $80 was paid out under a $100 limit.
	rec := record(t, 100, true, map[string]any{"account": "acct-1", "amount": 80.0})
	if rec.Output["paid"] != true {
		t.Fatalf("precondition: the original run should have paid, got %v", rec.Output)
	}

	// Tighten the limit to $50 and ask what that would have done.
	result, err, _ := replayAgainst(rec, 50)
	diffs := replay.Compare(rec, result, err)
	if len(diffs) == 0 {
		t.Fatal("tightening the limit past a recorded payout must show a difference")
	}

	var sawPaid, sawTrace bool
	for _, d := range diffs {
		switch d.Field {
		case "output.paid":
			sawPaid = true
			if d.Before != true || d.After != false {
				t.Fatalf("expected paid to flip true -> false, got %v", d)
			}
		case "trace":
			sawTrace = true
			if !strings.Contains(fmt.Sprint(d.After), "hold") {
				t.Fatalf("the new path should route to hold, got %v", d.After)
			}
		}
	}
	if !sawPaid || !sawTrace {
		t.Fatalf("expected both the decision and the path to be reported, got %v", diffs)
	}
}

// Raising the threshold should leave a payout that was already under it alone.
// A regression report that flags unaffected cases is noise.
func TestRaisingAThresholdLeavesUnaffectedDecisionsAlone(t *testing.T) {
	rec := record(t, 100, true, map[string]any{"account": "acct-1", "amount": 20.0})

	result, err, _ := replayAgainst(rec, 500)
	if diffs := replay.Compare(rec, result, err); len(diffs) != 0 {
		t.Fatalf("a $20 refund is unaffected by the limit moving to $500, got %v", diffs)
	}
}

// The probe read the world at record time. The world has since moved, and the
// replay must still reproduce the original run rather than consult it again.
func TestProbeVerdictsAreReplayedNotReread(t *testing.T) {
	// Recorded while the account was closed: the payout was held.
	rec := record(t, 100, false, map[string]any{"account": "acct-1", "amount": 20.0})
	if rec.Output["paid"] != false {
		t.Fatalf("precondition: a closed account should not have paid, got %v", rec.Output)
	}
	if len(rec.Probes) != 1 || rec.Probes[0].Healthy {
		t.Fatalf("expected one unhealthy verdict on the record, got %+v", rec.Probes)
	}

	// The account has since been reopened. Replaying must not notice.
	result, err, _ := replayAgainst(rec, 100)
	if diffs := replay.Compare(rec, result, err); len(diffs) != 0 {
		t.Fatalf("replay consulted the world instead of the record: %v", diffs)
	}
	if result.Output["paid"] == true {
		t.Fatal("the replay paid out a refund the original refused")
	}
}

// A recording is JSON so it can be filed next to the incident it belongs to.
// After a round trip every number is a float64, which must not read as a
// difference.
func TestRecordingSurvivesJSONRoundTrip(t *testing.T) {
	rec := record(t, 100, true, map[string]any{"account": "acct-1", "amount": 20.0})

	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var restored replay.Recording
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	result, runErr, _ := replayAgainst(restored, 100)
	if diffs := replay.Compare(restored, result, runErr); len(diffs) != 0 {
		t.Fatalf("a recording that went through JSON replayed differently: %v", diffs)
	}
}

// Failures are the runs most worth replaying, so a recording of one has to be
// as complete as a recording of a success.
func TestAFailedRunIsReplayable(t *testing.T) {
	// No fallback policy, so exceeding the limit trips the breaker outright.
	sandbox := proberFunc(func(entities.ProbeRequest) (entities.ProbeResult, error) {
		return entities.ProbeResult{Healthy: true}, nil
	})
	rec := replay.NewRecorder(sandbox)
	engine := execution.NewEngine(rec)
	engine.RegisterWorkflow(&execution.Workflow{
		WorkflowID:  "strict",
		EntryStepID: "pay",
		Steps: map[string]execution.Step{
			"pay": &execution.LeafStep{
				StepID:          "pay",
				SandboxProbeRef: "refund.account_open",
				Run: func(ctx map[string]any) (map[string]any, error) {
					return nil, errors.New("over the limit")
				},
			},
		},
	})
	input := map[string]any{"amount": 900.0}
	result, err := engine.Run("strict", input)
	if err == nil {
		t.Fatal("precondition: this run should have failed")
	}
	recording := rec.Finish("strict", input, result, err)
	if !recording.Failed || recording.Error == "" {
		t.Fatalf("the failure should be on the record, got %+v", recording)
	}

	// Same rule, replayed: the same failure, and no spurious differences.
	player := replay.NewPlayer(recording)
	engine2 := execution.NewEngine(player)
	engine2.RegisterWorkflow(&execution.Workflow{
		WorkflowID:  "strict",
		EntryStepID: "pay",
		Steps: map[string]execution.Step{
			"pay": &execution.LeafStep{
				StepID:          "pay",
				SandboxProbeRef: "refund.account_open",
				Run: func(ctx map[string]any) (map[string]any, error) {
					return nil, errors.New("over the limit")
				},
			},
		},
	})
	result2, err2 := engine2.Run("strict", input)
	if diffs := replay.Compare(recording, result2, err2); len(diffs) != 0 {
		t.Fatalf("a failed run replayed differently: %v", diffs)
	}
}

// An action that consults something other than its context is exactly what the
// L2 contract forbids, and the engine cannot see it. A replay can: the run
// diverges even though the rule and the recorded verdicts are unchanged.
func TestNondeterministicActionShowsUpAsDivergence(t *testing.T) {
	calls := 0
	build := func() *execution.Workflow {
		return &execution.Workflow{
			WorkflowID:  "flaky",
			EntryStepID: "act",
			Steps: map[string]execution.Step{
				"act": &execution.LeafStep{
					StepID: "act",
					Run: func(ctx map[string]any) (map[string]any, error) {
						calls++
						return map[string]any{"call_number": calls}, nil
					},
				},
			},
		}
	}

	rec := replay.NewRecorder(nil) // no probes in this workflow
	engine := execution.NewEngine(rec)
	engine.RegisterWorkflow(build())
	result, err := engine.Run("flaky", map[string]any{})
	recording := rec.Finish("flaky", map[string]any{}, result, err)

	engine2 := execution.NewEngine(replay.NewPlayer(recording))
	engine2.RegisterWorkflow(build())
	result2, err2 := engine2.Run("flaky", map[string]any{})

	diffs := replay.Compare(recording, result2, err2)
	if len(diffs) == 0 {
		t.Fatal("an action carrying state between runs should have been caught")
	}
	if diffs[0].Field != "output.call_number" {
		t.Fatalf("expected the drifting field to be named, got %v", diffs)
	}
}

// Refusing beats guessing: a probe the recording never captured cannot be
// reconstructed, and answering "healthy" would let a replay execute something
// the original never did.
func TestUnrecordedProbeIsRefusedAndReported(t *testing.T) {
	player := replay.NewPlayer(replay.Recording{WorkflowID: "w"})

	result, err := player.Probe(entities.ProbeRequest{ProbeRef: "never.seen"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Healthy {
		t.Fatal("an unrecorded probe must not be answered healthy")
	}
	if !strings.Contains(result.DetectedFailureMode, "no verdict") {
		t.Fatalf("the refusal should say why, got %q", result.DetectedFailureMode)
	}
	if got := player.Unmatched(); len(got) != 1 || got[0] != "never.seen" {
		t.Fatalf("the gap should be reported, got %v", got)
	}
}

// When a replay is deliberately exploring new ground, a fallback can answer
// what the recording does not hold.
func TestFallbackAnswersProbesTheRecordingLacks(t *testing.T) {
	player := replay.NewPlayer(replay.Recording{WorkflowID: "w"})
	player.Fallback = proberFunc(func(entities.ProbeRequest) (entities.ProbeResult, error) {
		return entities.ProbeResult{Healthy: true}, nil
	})

	result, err := player.Probe(entities.ProbeRequest{ProbeRef: "new.probe"})
	if err != nil || !result.Healthy {
		t.Fatalf("expected the fallback to answer, got %+v err=%v", result, err)
	}
	if len(player.Unmatched()) != 1 {
		t.Fatal("a fallback answer is still a gap in the recording and should be reported")
	}
}
