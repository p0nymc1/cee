// Package sandbox implements CEE's pre-execution sandbox: a gate that
// simulates a step's side effect before the deterministic engine commits to
// running it for real. The default implementation just calls a registered
// probe function in-process; swap it for an E2B/Docker-backed prober when a
// probe needs true process isolation.
package sandbox

import (
	"fmt"

	"github.com/cee-project/cee/entities"
)

// Probe simulates one step's side effect and reports whether it looks safe
// to run for real. A non-nil error is treated the same as returning false.
type Probe func(stepContext map[string]any) (healthy bool, failureMode string, err error)

// Sandbox holds the probes domains have registered.
type Sandbox struct {
	probes map[string]Probe
}

func NewSandbox() *Sandbox {
	return &Sandbox{probes: make(map[string]Probe)}
}

func (s *Sandbox) RegisterProbe(probeRef string, probe Probe) {
	s.probes[probeRef] = probe
}

// Probe satisfies execution.Prober: it never returns a Go error itself,
// folding any probe failure into ProbeResult.Healthy so the engine has a
// single failure path (the circuit breaker) to reason about.
func (s *Sandbox) Probe(req entities.ProbeRequest) (entities.ProbeResult, error) {
	probe, ok := s.probes[req.ProbeRef]
	if !ok {
		return entities.ProbeResult{
			Healthy:             false,
			DetectedFailureMode: fmt.Sprintf("no probe registered for %q", req.ProbeRef),
		}, nil
	}
	healthy, failureMode, err := probe(req.StepContext)
	if err != nil {
		return entities.ProbeResult{Healthy: false, DetectedFailureMode: err.Error()}, nil
	}
	if !healthy && failureMode == "" {
		failureMode = "probe reported unhealthy"
	}
	return entities.ProbeResult{Healthy: healthy, DetectedFailureMode: failureMode}, nil
}
