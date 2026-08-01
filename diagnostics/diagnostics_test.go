package diagnostics_test

import (
	"strings"
	"testing"

	"github.com/p0nymc1/cee/diagnostics"
	"github.com/p0nymc1/cee/entities"
	"github.com/p0nymc1/cee/execution"
	"github.com/p0nymc1/cee/intentrouter"
	"github.com/p0nymc1/cee/sandbox"
)

// The recorder is only useful if the router and engine will actually accept it.
// These assertions live in the test, not the package, so diagnostics stays a
// leaf that imports neither.
var (
	_ intentrouter.Observer          = (*diagnostics.Recorder)(nil)
	_ execution.Observer             = (*diagnostics.Recorder)(nil)
	_ execution.SuspensionObserver   = (*diagnostics.Recorder)(nil)
	_ execution.ProbeOutcomeObserver = (*diagnostics.Recorder)(nil)
)

func TestIntentMissRateCountsMissesAgainstAttempts(t *testing.T) {
	rec := diagnostics.NewRecorder()
	rec.ObserveMatch("finance", true)
	rec.ObserveMatch("finance", true)
	rec.ObserveMatch("finance", true)
	rec.ObserveMatch("finance", false)

	r := rec.Report()
	if r.IntentMatches != 3 || r.IntentMisses != 1 {
		t.Fatalf("matches=%d misses=%d, want 3 and 1", r.IntentMatches, r.IntentMisses)
	}
	if got := r.IntentMissRate(); got != 0.25 {
		t.Errorf("miss rate = %v, want 0.25", got)
	}
}

func TestProbeRefusalRateCountsRefusalsAgainstProbes(t *testing.T) {
	rec := diagnostics.NewRecorder()
	rec.ObserveProbeOutcome("wf", "s", true)
	rec.ObserveProbeOutcome("wf", "s", false)
	rec.ObserveProbeOutcome("wf", "s", true)
	rec.ObserveProbeOutcome("wf", "s", true)

	r := rec.Report()
	if r.ProbesRun != 4 || r.ProbesRefused != 1 {
		t.Fatalf("run=%d refused=%d, want 4 and 1", r.ProbesRun, r.ProbesRefused)
	}
	if got := r.ProbeRefusalRate(); got != 0.25 {
		t.Errorf("refusal rate = %v, want 0.25", got)
	}
}

func TestEscalationRateNeedsARunDenominator(t *testing.T) {
	rec := diagnostics.NewRecorder()
	rec.ObserveSuspension("wf", "hold")
	if got := rec.Report().EscalationRate(); got != 0 {
		t.Errorf("a suspension with no recorded run gives rate %v; a rate over no runs is no data, not a low rate", got)
	}

	rec.ObserveRun()
	rec.ObserveRun()
	if got := rec.Report().EscalationRate(); got != 0.5 {
		t.Errorf("escalation rate = %v, want 0.5 (one suspension over two runs)", got)
	}
}

func TestRatesAreZeroRatherThanNaNWithNoData(t *testing.T) {
	empty := diagnostics.NewRecorder().Report()
	for name, got := range map[string]float64{
		"intent miss":   empty.IntentMissRate(),
		"probe refusal": empty.ProbeRefusalRate(),
		"escalation":    empty.EscalationRate(),
	} {
		if got != 0 {
			t.Errorf("%s rate over no data = %v, want 0", name, got)
		}
	}
}

func TestReportStringReportsAllThreeRates(t *testing.T) {
	rec := diagnostics.NewRecorder()
	rec.ObserveMatch("d", false)
	rec.ObserveProbeOutcome("wf", "s", false)
	rec.ObserveRun()
	rec.ObserveSuspension("wf", "hold")

	s := rec.Report().String()
	for _, want := range []string{"intent miss", "probe refusal", "escalation"} {
		if !strings.Contains(s, want) {
			t.Errorf("String() missing %q: %s", want, s)
		}
	}
}

func TestRecorderRunsEndToEndOnARealRouterAndEngine(t *testing.T) {
	rec := diagnostics.NewRecorder()

	router := intentrouter.NewRouter(0.5)
	router.SetObserver(rec)
	router.RegisterNode(entities.IntentNode{
		NodeID:           "security.contain",
		DomainID:         "security",
		Examples:         []string{"isolate the compromised host now"},
		EntryWorkflowRef: "security.contain",
	})

	sb := sandbox.NewSandbox()
	sb.RegisterProbe("blast_radius", func(ctx map[string]any) (bool, string, error) {
		if ctx["host"] == "dc01" {
			return false, "isolating a domain controller would do more harm than the intrusion", nil
		}
		return true, "", nil
	})

	engine := execution.NewEngine(sb)
	engine.SetObserver(rec)
	engine.RegisterPolicy(execution.CircuitBreakerPolicy{PolicyID: "to_human", FallbackStepRef: "hold"})
	engine.RegisterWorkflow(&execution.Workflow{
		WorkflowID:  "security.contain",
		EntryStepID: "contain",
		Steps: map[string]execution.Step{
			"contain": &execution.LeafStep{
				StepID:                  "contain",
				SandboxProbeRef:         "blast_radius",
				CircuitBreakerPolicyRef: "to_human",
				Run: func(ctx map[string]any) (map[string]any, error) {
					return map[string]any{"contained": true}, nil
				},
			},
			"hold": &execution.LeafStep{
				StepID: "hold",
				Run: func(ctx map[string]any) (map[string]any, error) {
					return map[string]any{"held_for_analyst": true}, nil
				},
			},
		},
	})

	// A miss: nothing in the domain looks like this.
	router.Match("security", "the quarterly financial report is ready")
	// A hit: routes to the workflow.
	match := router.Match("security", "isolate the compromised host now")
	if !match.Matched {
		t.Fatal("expected the containment intent to match")
	}

	rec.ObserveRun()
	if _, err := engine.Run(match.EntryWorkflowRef, map[string]any{"host": "dc01"}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	r := rec.Report()
	if r.IntentMatches != 1 || r.IntentMisses != 1 {
		t.Errorf("intent: matches=%d misses=%d, want 1 and 1", r.IntentMatches, r.IntentMisses)
	}
	if r.ProbesRun != 1 || r.ProbesRefused != 1 {
		t.Errorf("probe: run=%d refused=%d, want 1 and 1", r.ProbesRun, r.ProbesRefused)
	}
	if r.BreakerTrips != 1 {
		t.Errorf("breaker trips = %d, want 1 (the probe refusal routed to a human)", r.BreakerTrips)
	}
	if r.Runs != 1 {
		t.Errorf("runs = %d, want 1", r.Runs)
	}
}
