package scorecard

import (
	"testing"

	"github.com/cee-project/cee/execution"
	"github.com/cee-project/cee/llminjector"
)

// The recorder must satisfy both observer interfaces. These assignments fail
// to compile if a method set ever drifts, which is the whole point of
// keeping them here rather than importing scorecard from those packages.
var (
	_ execution.Observer   = (*Recorder)(nil)
	_ llminjector.Observer = (*Recorder)(nil)
)

func TestDeterminismRatio(t *testing.T) {
	r := NewRecorder()
	r.ObserveStep("wf", "a")
	r.ObserveStep("wf", "b")
	r.ObserveStep("wf", "c")
	r.ObserveExtraction("schema") // one LLM call among four cognitive ops

	s := r.Snapshot("wf")
	if s.CognitiveOps() != 4 {
		t.Fatalf("expected 4 cognitive ops, got %d", s.CognitiveOps())
	}
	if got := s.DeterminismRatio(); got != 0.75 {
		t.Fatalf("expected determinism 0.75, got %v", got)
	}
	if s.LLMCallsEliminatedVsAgent() != 3 {
		t.Fatalf("expected 3 LLM calls eliminated, got %d", s.LLMCallsEliminatedVsAgent())
	}
}

func TestEmptyRunIsFullyDeterministic(t *testing.T) {
	s := NewRecorder().Snapshot("wf")
	if s.DeterminismRatio() != 1 {
		t.Fatalf("expected empty run to be 100%% deterministic, got %v", s.DeterminismRatio())
	}
}

func TestProbesAndBreakersAreCounted(t *testing.T) {
	r := NewRecorder()
	r.ObserveSandboxProbe("wf", "contain")
	r.ObserveCircuitBreaker("wf", "contain")
	s := r.Snapshot("wf")
	if s.SandboxProbes != 1 || s.CircuitBreakerTrips != 1 {
		t.Fatalf("expected 1 probe and 1 breaker trip, got %+v", s)
	}
}
