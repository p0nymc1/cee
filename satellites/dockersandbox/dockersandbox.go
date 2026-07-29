// Package dockersandbox is an optional, satellite implementation of
// execution.Prober that rehearses a step's side effect inside a throwaway
// Docker container, so a candidate command runs in real isolation instead of
// merely being simulated in-process.
//
// It lives in its own Go module on purpose. The core cee module depends only
// on the standard library; anything that needs a heavier backend -- a
// container runtime here, the E2B SDK or a WASM runtime elsewhere -- belongs
// in a satellite module with its own go.mod, so those dependencies never
// reach the core. A satellite plugs in through the same execution.Prober
// interface the in-process sandbox uses, so the engine is unchanged.
//
// (This particular satellite happens to shell out to the `docker` CLI via
// os/exec and so is itself dependency-free; a satellite built on the E2B Go
// SDK or a WASM runtime would carry those requires in this same go.mod, and
// still leave the core untouched.)
package dockersandbox

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/p0nymc1/cee/entities"
	"github.com/p0nymc1/cee/execution"
)

// CommandRunner runs an external command and reports its exit code and
// combined output. A non-zero exit code is a normal result, not a Go error;
// err is reserved for "the command could not be started at all" (e.g. docker
// is not installed). Tests inject a fake so the suite stays hermetic.
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

// Sandbox is an execution.Prober backed by Docker.
type Sandbox struct {
	Image  string
	Runner CommandRunner
}

// Compile-time proof this satisfies the core sandbox interface unchanged.
var _ execution.Prober = (*Sandbox)(nil)

// New builds a Sandbox that rehearses probes in the given image using the
// real `docker` CLI.
func New(image string) *Sandbox {
	return &Sandbox{Image: image, Runner: execRunner{}}
}

// Probe runs the step context's probe_command inside a throwaway, networkless
// container. Exit 0 means healthy; a non-zero exit or an unavailable runtime
// means unhealthy, which the engine routes through the step's circuit
// breaker exactly as with the in-process sandbox.
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

// probeCommand pulls a command out of the step context, accepting both a
// Go-set []string and a JSON-set []any.
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
