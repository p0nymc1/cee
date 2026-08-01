// Package diagnostics measures the error side of a deployment: how often intent
// routing misses, how often a pre-execution probe refuses, and how often a run
// escalates to a human. The scorecard measures output -- deterministic steps
// run, model calls eliminated -- which are the flattering numbers. Measuring
// only those is a form of bias, so this package measures the numbers a bad
// deployment would rather you did not see.
//
// A Recorder aggregates across many runs rather than scoring one, which is the
// difference from scorecard.Recorder. Attach it to the router (for intent
// hit/miss), to the engine (for probe outcomes, suspensions and breaker trips),
// and call ObserveRun once per top-level request so an escalation rate has a
// denominator. It satisfies intentrouter.Observer, execution.Observer,
// execution.SuspensionObserver and execution.ProbeOutcomeObserver structurally,
// so this package imports none of them and stays a leaf.
package diagnostics

import (
	"fmt"
	"sync"
)

// Report is a snapshot of the counters. The three rates are the point; the raw
// counts are exposed so a caller can see what they were computed from rather
// than trusting a ratio with no denominator.
type Report struct {
	IntentMatches int
	IntentMisses  int

	ProbesRun     int
	ProbesRefused int

	Runs         int
	Suspensions  int
	BreakerTrips int
}

// IntentMissRate is the share of routing attempts that matched no intent. A
// high value means natural-language inputs are reaching the router that no
// domain declared an example for -- the thing to fix before quoting a hit rate.
// Zero when nothing was routed, because a rate over no attempts is not a low
// miss rate, it is no data.
func (r Report) IntentMissRate() float64 {
	total := r.IntentMatches + r.IntentMisses
	if total == 0 {
		return 0
	}
	return float64(r.IntentMisses) / float64(total)
}

// ProbeRefusalRate is the share of probes that refused their step. This is not
// a failure rate to drive down: a probe refusing is the sandbox doing its job.
// It is a rate to watch, because a sudden change in it is a change in what the
// world looks like to the probes.
func (r Report) ProbeRefusalRate() float64 {
	if r.ProbesRun == 0 {
		return 0
	}
	return float64(r.ProbesRefused) / float64(r.ProbesRun)
}

// EscalationRate is suspensions per run -- how often a request could not be
// finished without a person. It needs ObserveRun to have been called, because
// the engine does not know what a caller counts as one request; a nested or
// parallel run is not obviously one or several. Zero when no run was recorded.
func (r Report) EscalationRate() float64 {
	if r.Runs == 0 {
		return 0
	}
	return float64(r.Suspensions) / float64(r.Runs)
}

func (r Report) String() string {
	return fmt.Sprintf(
		"diagnostics: intent miss %.0f%% (%d of %d), probe refusal %.0f%% (%d of %d), "+
			"escalation %.0f%% (%d of %d runs), %d breaker trips",
		r.IntentMissRate()*100, r.IntentMisses, r.IntentMatches+r.IntentMisses,
		r.ProbeRefusalRate()*100, r.ProbesRefused, r.ProbesRun,
		r.EscalationRate()*100, r.Suspensions, r.Runs,
		r.BreakerTrips,
	)
}

// Recorder accumulates the diagnostic counters. It is safe for concurrent use,
// which it has to be: once workflows fan out, the engine calls an observer from
// several goroutines at once.
type Recorder struct {
	mu sync.Mutex
	r  Report
}

func NewRecorder() *Recorder { return &Recorder{} }

// ObserveRun marks one top-level request. The caller owns the definition of a
// request, so the caller makes this call -- typically once where it hands work
// to the engine, such as an HTTP handler per inbound call.
func (rec *Recorder) ObserveRun() {
	rec.mu.Lock()
	rec.r.Runs++
	rec.mu.Unlock()
}

// ObserveMatch satisfies intentrouter.Observer.
func (rec *Recorder) ObserveMatch(domainID string, matched bool) {
	rec.mu.Lock()
	if matched {
		rec.r.IntentMatches++
	} else {
		rec.r.IntentMisses++
	}
	rec.mu.Unlock()
}

// ObserveProbeOutcome satisfies execution.ProbeOutcomeObserver.
func (rec *Recorder) ObserveProbeOutcome(workflowID, stepID string, healthy bool) {
	rec.mu.Lock()
	rec.r.ProbesRun++
	if !healthy {
		rec.r.ProbesRefused++
	}
	rec.mu.Unlock()
}

// ObserveSuspension satisfies execution.SuspensionObserver.
func (rec *Recorder) ObserveSuspension(workflowID, stepID string) {
	rec.mu.Lock()
	rec.r.Suspensions++
	rec.mu.Unlock()
}

// ObserveCircuitBreaker satisfies execution.Observer.
func (rec *Recorder) ObserveCircuitBreaker(workflowID, stepID string) {
	rec.mu.Lock()
	rec.r.BreakerTrips++
	rec.mu.Unlock()
}

// ObserveStep satisfies execution.Observer. Deterministic step counts are the
// scorecard's job; here it is a no-op that exists only to complete the
// interface so the same recorder can be the engine's observer.
func (rec *Recorder) ObserveStep(workflowID, stepID string) {}

// ObserveSandboxProbe satisfies execution.Observer. The probe outcome, not the
// bare fact that a probe ran, is what this package counts, so this is a no-op.
func (rec *Recorder) ObserveSandboxProbe(workflowID, stepID string) {}

// Report returns a snapshot of the counters so far.
func (rec *Recorder) Report() Report {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return rec.r
}
