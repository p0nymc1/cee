package httpsandbox

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/p0nymc1/cee/entities"
	"github.com/p0nymc1/cee/execution"
)

type fakeDoer struct {
	status    int
	resp      rehearseResponse
	transport error
	lastBody  string
	lastAuth  string
	lastURL   string
}

func (f *fakeDoer) Do(req *http.Request) (*http.Response, error) {
	if f.transport != nil {
		return nil, f.transport
	}
	if req.Body != nil {
		b, _ := io.ReadAll(req.Body)
		f.lastBody = string(b)
	}
	f.lastAuth = req.Header.Get("Authorization")
	f.lastURL = req.URL.String()
	body, _ := json.Marshal(f.resp)
	return &http.Response{
		StatusCode: f.status,
		Body:       io.NopCloser(strings.NewReader(string(body))),
		Header:     make(http.Header),
	}, nil
}

func TestHealthyRehearsalSendsCommandAndImage(t *testing.T) {
	doer := &fakeDoer{status: 200, resp: rehearseResponse{ExitCode: 0}}
	sb := New(Config{BaseURL: "https://sbx/", APIKey: "secret", Image: "python:3", HTTPClient: doer})

	res, err := sb.Probe(entities.ProbeRequest{StepContext: map[string]any{"probe_command": []string{"python", "-c", "1"}}})
	if err != nil || !res.Healthy {
		t.Fatalf("expected healthy, got %+v err=%v", res, err)
	}
	if doer.lastAuth != "Bearer secret" {
		t.Fatalf("API key not sent as bearer token, got %q", doer.lastAuth)
	}
	if !strings.HasSuffix(doer.lastURL, "/rehearse") {
		t.Fatalf("expected POST to /rehearse, got %q", doer.lastURL)
	}
	for _, want := range []string{"python:3", "\"python\"", "-c"} {
		if !strings.Contains(doer.lastBody, want) {
			t.Fatalf("request body missing %q: %s", want, doer.lastBody)
		}
	}
}

func TestNonZeroExitIsUnhealthy(t *testing.T) {
	sb := New(Config{BaseURL: "https://sbx", HTTPClient: &fakeDoer{status: 200, resp: rehearseResponse{ExitCode: 2, Output: "would drop the table"}}})
	res, _ := sb.Probe(entities.ProbeRequest{StepContext: map[string]any{"probe_command": []string{"psql"}}})
	if res.Healthy {
		t.Fatalf("expected unhealthy for non-zero exit")
	}
	if !strings.Contains(res.DetectedFailureMode, "would drop the table") {
		t.Fatalf("failure mode should carry the sandbox output, got %q", res.DetectedFailureMode)
	}
}

func TestServiceUnavailableIsUnhealthyNotAnError(t *testing.T) {
	sb := New(Config{BaseURL: "https://sbx", HTTPClient: &fakeDoer{transport: errors.New("no route to host")}})
	res, err := sb.Probe(entities.ProbeRequest{StepContext: map[string]any{"probe_command": []string{"true"}}})
	if err != nil {
		t.Fatalf("an unreachable service should fold into the result, not a Go error: %v", err)
	}
	if res.Healthy {
		t.Fatalf("expected unhealthy when the service is unreachable")
	}
}

func TestNon200IsUnhealthy(t *testing.T) {
	sb := New(Config{BaseURL: "https://sbx", HTTPClient: &fakeDoer{status: 503, resp: rehearseResponse{}}})
	res, _ := sb.Probe(entities.ProbeRequest{StepContext: map[string]any{"probe_command": []string{"true"}}})
	if res.Healthy {
		t.Fatalf("expected unhealthy for a non-200 status")
	}
}

func TestMissingCommandIsUnhealthy(t *testing.T) {
	sb := New(Config{BaseURL: "https://sbx", HTTPClient: &fakeDoer{status: 200}})
	res, _ := sb.Probe(entities.ProbeRequest{StepContext: map[string]any{}})
	if res.Healthy {
		t.Fatalf("expected unhealthy when no probe_command is present")
	}
}

func TestSatellitePlugsIntoTheEngineUnchanged(t *testing.T) {
	build := func(doer Doer) *execution.Engine {
		engine := execution.NewEngine(New(Config{BaseURL: "https://sbx", HTTPClient: doer}))
		engine.RegisterPolicy(execution.CircuitBreakerPolicy{PolicyID: "hold", FallbackStepRef: "held"})
		engine.RegisterWorkflow(&execution.Workflow{
			WorkflowID:  "migrate",
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

	ok, err := build(&fakeDoer{status: 200, resp: rehearseResponse{ExitCode: 0}}).
		Run("migrate", map[string]any{"probe_command": []string{"migrate", "--dry-run"}})
	if err != nil || ok.Output["applied"] != true {
		t.Fatalf("expected applied=true after a healthy rehearsal, got %+v err=%v", ok.Output, err)
	}

	bad, err := build(&fakeDoer{status: 200, resp: rehearseResponse{ExitCode: 1, Output: "constraint violation"}}).
		Run("migrate", map[string]any{"probe_command": []string{"migrate", "--dry-run"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if bad.Output["applied"] == true {
		t.Fatalf("a failed rehearsal must not let the migration run")
	}
	if bad.Output["held"] != true {
		t.Fatalf("expected the breaker to route to held, got %+v", bad.Output)
	}
}

type endlessBody struct{}

func (endlessBody) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'a'
	}
	return len(p), nil
}

type oversizedDoer struct{}

func (oversizedDoer) Do(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: 200, Body: io.NopCloser(endlessBody{}), Header: make(http.Header)}, nil
}

func TestProbeRejectsAnOversizedResponse(t *testing.T) {
	sb := New(Config{BaseURL: "https://sbx", Image: "python:3", HTTPClient: oversizedDoer{}})
	result, err := sb.Probe(entities.ProbeRequest{StepContext: map[string]any{"probe_command": []string{"echo", "hi"}}})
	if err != nil {
		t.Fatalf("Probe should fold the limit into a result, not error: %v", err)
	}
	if result.Healthy || !strings.Contains(result.DetectedFailureMode, "limit") {
		t.Fatalf("an unbounded sandbox response must be refused, got %+v", result)
	}
}
