// Package scorecard measures a single request's execution so the community
// can compare a CEE plugin against a naive-agent baseline on hard numbers
// instead of a slogan.
//
// The baseline model is deliberate and honest: a naive agent makes one LLM
// call per step. Under that model, every deterministic step the engine runs
// is exactly one LLM call CEE eliminated. So the headline metric --
// DeterminismRatio -- is not an estimate; it is the fraction of would-be LLM
// calls that never happened. No token guessing is required for this to be
// meaningful, and it becomes even sharper once a real tokenizing LLM is
// wired into the injector.
//
// A Recorder implements both execution.Observer and llminjector.Observer
// structurally (matching method sets), so this package imports neither --
// it stays a leaf with no dependency on the engine it measures.
package scorecard

import (
	"fmt"
	"sync"
	"time"
)

// Scorecard is the immutable summary of one measured request.
type Scorecard struct {
	WorkflowID          string
	DeterministicSteps  int           // engine steps that ran without any LLM
	LLMCalls            int           // edge LLM extraction invocations
	SandboxProbes       int           // pre-execution simulations performed
	CircuitBreakerTrips int           // failures rerouted to a fallback
	Duration            time.Duration // wall-clock time from Start to snapshot
}

// CognitiveOps is the total of deterministic steps plus LLM calls -- the
// denominator a naive per-step agent would have paid entirely in LLM calls.
func (s Scorecard) CognitiveOps() int {
	return s.DeterministicSteps + s.LLMCalls
}

// DeterminismRatio is the fraction of cognitive operations handled
// deterministically. Interpreted against the per-step-agent baseline, it is
// the fraction of LLM calls CEE eliminated. Returns 1 for an empty run.
func (s Scorecard) DeterminismRatio() float64 {
	total := s.CognitiveOps()
	if total == 0 {
		return 1
	}
	return float64(s.DeterministicSteps) / float64(total)
}

// LLMCallsEliminatedVsAgent is how many LLM calls a naive per-step agent
// would have made that CEE did not: exactly the deterministic step count.
func (s Scorecard) LLMCallsEliminatedVsAgent() int {
	return s.DeterministicSteps
}

// String renders a compact, human-readable summary.
func (s Scorecard) String() string {
	return fmt.Sprintf(
		"scorecard[%s]: determinism %.0f%% (%d deterministic steps, %d LLM calls), "+
			"%d sandbox probes, %d breaker trips, %s; vs a per-step agent this eliminated %d LLM calls",
		s.WorkflowID, s.DeterminismRatio()*100, s.DeterministicSteps, s.LLMCalls,
		s.SandboxProbes, s.CircuitBreakerTrips, s.Duration.Round(time.Microsecond),
		s.LLMCallsEliminatedVsAgent(),
	)
}

// Recorder accumulates observed events for one request. It is safe for
// concurrent use so a workflow that fans out across goroutines still counts
// correctly. Create one per request with NewRecorder, attach it to the
// engine and injector, then call Snapshot when the request finishes.
type Recorder struct {
	start time.Time

	mu                  sync.Mutex
	deterministicSteps  int
	llmCalls            int
	sandboxProbes       int
	circuitBreakerTrips int
}

// NewRecorder starts a recorder and its clock.
func NewRecorder() *Recorder {
	return &Recorder{start: time.Now()}
}

// ObserveStep satisfies execution.Observer.
func (r *Recorder) ObserveStep(workflowID, stepID string) {
	r.mu.Lock()
	r.deterministicSteps++
	r.mu.Unlock()
}

// ObserveSandboxProbe satisfies execution.Observer.
func (r *Recorder) ObserveSandboxProbe(workflowID, stepID string) {
	r.mu.Lock()
	r.sandboxProbes++
	r.mu.Unlock()
}

// ObserveCircuitBreaker satisfies execution.Observer.
func (r *Recorder) ObserveCircuitBreaker(workflowID, stepID string) {
	r.mu.Lock()
	r.circuitBreakerTrips++
	r.mu.Unlock()
}

// ObserveExtraction satisfies llminjector.Observer.
func (r *Recorder) ObserveExtraction(schemaRef string) {
	r.mu.Lock()
	r.llmCalls++
	r.mu.Unlock()
}

// Snapshot returns the scorecard as of now, stamping elapsed time. The
// recorder may keep being used afterward; a later Snapshot reflects the
// additional events.
func (r *Recorder) Snapshot(workflowID string) Scorecard {
	r.mu.Lock()
	defer r.mu.Unlock()
	return Scorecard{
		WorkflowID:          workflowID,
		DeterministicSteps:  r.deterministicSteps,
		LLMCalls:            r.llmCalls,
		SandboxProbes:       r.sandboxProbes,
		CircuitBreakerTrips: r.circuitBreakerTrips,
		Duration:            time.Since(r.start),
	}
}
