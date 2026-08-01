package execution

import "testing"

type probeOutcomeRecorder struct {
	stepObserver
	outcomes []bool
}

// stepObserver is the minimal base Observer; only the probe-outcome method
// carries information for this test.
type stepObserver struct{}

func (stepObserver) ObserveStep(string, string)           {}
func (stepObserver) ObserveSandboxProbe(string, string)   {}
func (stepObserver) ObserveCircuitBreaker(string, string) {}

func (r *probeOutcomeRecorder) ObserveProbeOutcome(workflowID, stepID string, healthy bool) {
	r.outcomes = append(r.outcomes, healthy)
}

func probeGatedWorkflow() *Workflow {
	return &Workflow{
		WorkflowID:  "gated",
		EntryStepID: "risky",
		Steps: map[string]Step{
			"risky": &LeafStep{
				StepID:                  "risky",
				SandboxProbeRef:         "check_impact",
				CircuitBreakerPolicyRef: "escalate",
				Run: func(ctx map[string]any) (map[string]any, error) {
					return map[string]any{"executed": true}, nil
				},
			},
			"human_review": &LeafStep{
				StepID: "human_review",
				Run: func(ctx map[string]any) (map[string]any, error) {
					return map[string]any{"escalated": true}, nil
				},
			},
		},
	}
}

func TestProbeOutcomeIsReportedAsHealthyWhenTheProbePasses(t *testing.T) {
	rec := &probeOutcomeRecorder{}
	engine := NewEngine(&fakeSandbox{healthy: true})
	engine.SetObserver(rec)
	engine.RegisterPolicy(CircuitBreakerPolicy{PolicyID: "escalate", FallbackStepRef: "human_review"})
	engine.RegisterWorkflow(probeGatedWorkflow())

	if _, err := engine.Run("gated", map[string]any{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(rec.outcomes) != 1 || rec.outcomes[0] != true {
		t.Errorf("outcomes = %v, want [true]", rec.outcomes)
	}
}

func TestProbeOutcomeIsReportedAsRefusalWhenTheProbeRefuses(t *testing.T) {
	rec := &probeOutcomeRecorder{}
	engine := NewEngine(&fakeSandbox{healthy: false, failureMode: "would isolate a domain controller"})
	engine.SetObserver(rec)
	engine.RegisterPolicy(CircuitBreakerPolicy{PolicyID: "escalate", FallbackStepRef: "human_review"})
	engine.RegisterWorkflow(probeGatedWorkflow())

	if _, err := engine.Run("gated", map[string]any{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(rec.outcomes) != 1 || rec.outcomes[0] != false {
		t.Errorf("outcomes = %v, want [false]; a refusal is the signal a bare breaker count cannot give", rec.outcomes)
	}
}

func TestAnObserverWithoutTheOptionalMethodStillWorks(t *testing.T) {
	// stepObserver alone does not implement ProbeOutcomeObserver; the engine
	// must not require it.
	engine := NewEngine(&fakeSandbox{healthy: true})
	engine.SetObserver(stepObserver{})
	engine.RegisterPolicy(CircuitBreakerPolicy{PolicyID: "escalate", FallbackStepRef: "human_review"})
	engine.RegisterWorkflow(probeGatedWorkflow())

	if _, err := engine.Run("gated", map[string]any{}); err != nil {
		t.Fatalf("Run must not depend on the optional observer: %v", err)
	}
}
