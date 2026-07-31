package dockersandbox

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/p0nymc1/cee/entities"
	"github.com/p0nymc1/cee/execution"
)

type CommandRunner interface {
	Run(name string, args ...string) (exitCode int, output string, err error)
}

type execRunner struct{}

func (execRunner) Run(name string, args ...string) (int, string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode(), string(out), nil
		}
		return -1, string(out), err
	}
	return 0, string(out), nil
}

type Sandbox struct {
	Image  string
	Runner CommandRunner
}

var _ execution.Prober = (*Sandbox)(nil)

func New(image string) *Sandbox {
	return &Sandbox{Image: image, Runner: execRunner{}}
}

func (s *Sandbox) Probe(req entities.ProbeRequest) (entities.ProbeResult, error) {
	command, ok := probeCommand(req.StepContext)
	if !ok {
		return entities.ProbeResult{
			Healthy:             false,
			DetectedFailureMode: "dockersandbox: step context has no probe_command []string",
		}, nil
	}

	args := append([]string{"run", "--rm", "--network=none", s.Image}, command...)
	exitCode, output, err := s.Runner.Run("docker", args...)
	if err != nil {
		return entities.ProbeResult{
			Healthy:             false,
			DetectedFailureMode: "dockersandbox: docker unavailable: " + err.Error(),
		}, nil
	}
	if exitCode != 0 {
		return entities.ProbeResult{
			Healthy:             false,
			DetectedFailureMode: fmt.Sprintf("dockersandbox: probe exited %d: %s", exitCode, strings.TrimSpace(output)),
		}, nil
	}
	return entities.ProbeResult{Healthy: true}, nil
}

func probeCommand(ctx map[string]any) ([]string, bool) {
	switch v := ctx["probe_command"].(type) {
	case []string:
		return v, len(v) > 0
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			s, ok := item.(string)
			if !ok {
				return nil, false
			}
			out = append(out, s)
		}
		return out, len(out) > 0
	default:
		return nil, false
	}
}
