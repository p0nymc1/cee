package sandbox

import (
	"fmt"
	"sync"

	"github.com/p0nymc1/cee/entities"
)

type Probe func(stepContext map[string]any) (healthy bool, failureMode string, err error)

type Sandbox struct {
	mu     sync.RWMutex
	probes map[string]Probe
}

func NewSandbox() *Sandbox {
	return &Sandbox{probes: make(map[string]Probe)}
}

func (s *Sandbox) RegisterProbe(probeRef string, probe Probe) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.probes[probeRef] = probe
}

func (s *Sandbox) Probe(req entities.ProbeRequest) (entities.ProbeResult, error) {
	s.mu.RLock()
	probe, ok := s.probes[req.ProbeRef]
	s.mu.RUnlock()
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
