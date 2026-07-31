package scorecard

import (
	"fmt"
	"sync"
	"time"
)

type Scorecard struct {
	WorkflowID          string
	DeterministicSteps  int
	LLMCalls            int
	SandboxProbes       int
	CircuitBreakerTrips int
	Duration            time.Duration
}

func (s Scorecard) CognitiveOps() int {
	return s.DeterministicSteps + s.LLMCalls
}

func (s Scorecard) DeterminismRatio() float64 {
	total := s.CognitiveOps()
	if total == 0 {
		return 1
	}
	return float64(s.DeterministicSteps) / float64(total)
}

func (s Scorecard) LLMCallsEliminatedVsAgent() int {
	return s.DeterministicSteps
}

func (s Scorecard) String() string {
	return fmt.Sprintf(
		"scorecard[%s]: determinism %.0f%% (%d deterministic steps, %d LLM calls), "+
			"%d sandbox probes, %d breaker trips, %s; vs a per-step agent this eliminated %d LLM calls",
		s.WorkflowID, s.DeterminismRatio()*100, s.DeterministicSteps, s.LLMCalls,
		s.SandboxProbes, s.CircuitBreakerTrips, s.Duration.Round(time.Microsecond),
		s.LLMCallsEliminatedVsAgent(),
	)
}

type Recorder struct {
	start time.Time

	mu                  sync.Mutex
	deterministicSteps  int
	llmCalls            int
	sandboxProbes       int
	circuitBreakerTrips int
}

func NewRecorder() *Recorder {
	return &Recorder{start: time.Now()}
}

func (r *Recorder) ObserveStep(workflowID, stepID string) {
	r.mu.Lock()
	r.deterministicSteps++
	r.mu.Unlock()
}

func (r *Recorder) ObserveSandboxProbe(workflowID, stepID string) {
	r.mu.Lock()
	r.sandboxProbes++
	r.mu.Unlock()
}

func (r *Recorder) ObserveCircuitBreaker(workflowID, stepID string) {
	r.mu.Lock()
	r.circuitBreakerTrips++
	r.mu.Unlock()
}

func (r *Recorder) ObserveExtraction(schemaRef string) {
	r.mu.Lock()
	r.llmCalls++
	r.mu.Unlock()
}

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
