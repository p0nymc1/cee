package dockersandbox

import (
	"errors"
	"strings"
	"testing"

	"github.com/cee-project/cee/entities"
	"github.com/cee-project/cee/execution"
)

// fakeRunner stands in for the docker CLI so the suite is hermetic and needs
// no container runtime.
type fakeRunner struct {
	exit     int
	output   string
	err      error
	lastArgs []string
}

func (f *fakeRunner) Run(name string, args ...string) (int, string, error) {
	f.lastArgs = append([]string{name}, args...)
	return f.exit, f.output, f.err
}

func TestHealthyProbeBuildsAnIsolatedRun(t *testing.T) {
	runner := &fakeRunner{exit: 0}
	sb := &Sandbox{Image: "alpine", Runner: runner}

	res, err := sb.Probe(entities.ProbeRequest{StepContext: map[string]any{"probe_command": []string{"true"}}})
	if err != nil || !res.Healthy {
		t.Fatalf("expected healthy, got %+v err=%v", res, err)
	}
	joined := strings.Join(runner.lastArgs, " ")
	for _, want := range []string{"docker run", "--rm", "--network=none", "alpine", "true"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected docker invocation to contain %q, got: %s", want, joined)
		}
	}
}

func TestNonZeroExitIsUnhealthy(t *testing.T) {
	sb := &Sandbox{Image: "alpine", Runner: &fakeRunner{exit: 1, output: "would clobber prod"}}
	res, _ := sb.Probe(entities.ProbeRequest{StepContext: map[string]any{"probe_command": []string{"false"}}})
	if res.Healthy {
		t.Fatalf("expected unhealthy for non-zero exit")
	}
	if !strings.Contains(res.DetectedFailureMode, "would clobber prod") {
		t.Fatalf("failure mode should carry the probe output, got %q", res.DetectedFailureMode)
	}
}

func TestDockerUnavailableIsUnhealthyNotAnError(t *testing.T) {
	sb := &Sandbox{Image: "alpine", Runner: &fakeRunner{err: errors.New("exec: \"docker\": not found")}}
	res, err := sb.Probe(entities.ProbeRequest{StepContext: map[string]any{"probe_command": []string{"true"}}})
	if err != nil {
		t.Fatalf("a missing runtime should fold into the result, not a Go error: %v", err)
	}
	if res.Healthy {
		t.Fatalf("expected unhealthy when docker is unavailable")
	}
}

func TestMissingCommandIsUnhealthy(t *testing.T) {
	sb := &Sandbox{Image: "alpine", Runner: &fakeRunner{exit: 0}}
	res, _ := sb.Probe(entities.ProbeRequest{StepContext: map[string]any{}})
	if res.Healthy {
		t.Fatalf("expected unhealthy when no probe_command is present")
	}
}

func TestJSONStyleCommandIsAccepted(t *testing.T) {
	runner := &fakeRunner{exit: 0}
	sb := &Sandbox{Image: "alpine", Runner: runner}
	// A manifest-set context arrives as []any, not []string.
	res, _ := sb.Probe(entities.ProbeRequest{StepContext: map[string]any{"probe_command": []any{"echo", "hi"}}})
	if !res.Healthy {
		t.Fatalf("expected healthy for a JSON-style []any command")
	}
}

// The whole point of a satellite: it drops into the real engine through the
// existing execution.Prober interface, with no change to the core. A healthy
// probe lets the step run; an unhealthy one gates it through the circuit
// breaker exactly as the in-process sandbox would.
func TestSatellitePlugsIntoTheEngineUnchanged(t *testing.T) {
	build := func(runner CommandRunner) *execution.Engine {
		engine := execution.NewEngine(&Sandbox{Image: "alpine", Runner: runner})
		engine.RegisterPolicy(execution.CircuitBreakerPolicy{PolicyID: "hold", FallbackStepRef: "held"})
		engine.RegisterWorkflow(&execution.Workflow{
			WorkflowID:  "deploy",
			EntryStepID: "apply",
			Steps: map[string]execution.Step{
				"apply": &execution.LeafStep{
					StepID:                  "apply",
					SandboxProbeRef:         "rehearse",
					CircuitBreakerPolicyRef: "hold",
					Run: func(ctx map[string]any) (map[string]any, error) {
						return map[string]any{"applied": true}, nil
					},
				},
				"held": &execution.LeafStep{
					StepID: "held",
					Run: func(ctx map[string]any) (map[string]any, error) {
						return map[string]any{"held": true}, nil
					},
				},
			},
		})
		return engine
	}

	// Rehearsal passes -> the real action runs.
	ok, err := build(&fakeRunner{exit: 0}).Run("deploy", map[string]any{"probe_command": []string{"true"}})
	if err != nil || ok.Output["applied"] != true {
		t.Fatalf("expected applied=true after a healthy rehearsal, got %+v err=%v", ok.Output, err)
	}

	// Rehearsal fails -> the step is gated, breaker routes to held.
	bad, err := build(&fakeRunner{exit: 1, output: "syntax error"}).Run("deploy", map[string]any{"probe_command": []string{"false"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if bad.Output["applied"] == true {
		t.Fatalf("a failed rehearsal must not let the action run")
	}
	if bad.Output["held"] != true {
		t.Fatalf("expected the breaker to route to held, got %+v", bad.Output)
	}
}
