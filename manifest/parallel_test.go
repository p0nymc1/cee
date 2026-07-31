package manifest

import (
	"os"
	"strings"
	"testing"

	"github.com/p0nymc1/cee/execution"
	"github.com/p0nymc1/cee/intentrouter"
	"github.com/p0nymc1/cee/registry"
	"github.com/p0nymc1/cee/stdlib"
)

const parallelManifestJSON = `{
  "name": "checks",
  "workflows": [
    {"workflow_id": "checks.main", "entry_step_id": "gather", "steps": [
      {"step_id": "gather", "type": "parallel", "branches": ["checks.a", "checks.b"], "on_success": "done"},
      {"step_id": "done", "type": "leaf", "action_ref": "std.set", "with": {"fields": {"joined": true}}}
    ]},
    {"workflow_id": "checks.a", "entry_step_id": "a", "steps": [
      {"step_id": "a", "type": "leaf", "action_ref": "std.set", "with": {"fields": {"from_a": 1}}}
    ]},
    {"workflow_id": "checks.b", "entry_step_id": "b", "steps": [
      {"step_id": "b", "type": "leaf", "action_ref": "std.set", "with": {"fields": {"from_b": 2}}}
    ]}
  ]
}`

func loadAndRun(t *testing.T, manifestJSON, workflowRef string, ctx map[string]any) (map[string]any, error) {
	t.Helper()
	domain, err := Load([]byte(manifestJSON), nil, stdlib.Default())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	engine := execution.NewEngine(nil)
	reg := registry.NewRegistry(intentrouter.NewRouter(0.5), engine)
	reg.RegisterDomain(*domain)

	result, err := engine.Run(workflowRef, ctx)
	return result.Output, err
}

func TestAParallelStepLoadsAndRunsFromPureJSON(t *testing.T) {
	output, err := loadAndRun(t, parallelManifestJSON, "checks.main", map[string]any{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for field, want := range map[string]any{"from_a": 1.0, "from_b": 2.0, "joined": true} {
		if output[field] != want {
			t.Errorf("output[%q] = %v, want %v", field, output[field], want)
		}
	}
}

func TestLoadRejectsAParallelStepWithNoBranches(t *testing.T) {
	_, err := Load([]byte(`{"name":"x","workflows":[
      {"workflow_id":"x.main","entry_step_id":"g","steps":[
        {"step_id":"g","type":"parallel"}]}]}`), nil, stdlib.Default())
	if err == nil || !strings.Contains(err.Error(), "no branches") {
		t.Fatalf("want a no-branches error, got %v", err)
	}
}

func TestLoadRejectsADuplicatedBranch(t *testing.T) {
	_, err := Load([]byte(`{"name":"x","workflows":[
      {"workflow_id":"x.main","entry_step_id":"g","steps":[
        {"step_id":"g","type":"parallel","branches":["x.a","x.a"]}]},
      {"workflow_id":"x.a","entry_step_id":"a","steps":[
        {"step_id":"a","type":"leaf","action_ref":"std.set","with":{"fields":{"n":1}}}]}]}`),
		nil, stdlib.Default())
	if err == nil || !strings.Contains(err.Error(), "twice") {
		t.Fatalf("want a duplicate-branch error, got %v", err)
	}
}

func TestValidateCatchesADanglingBranch(t *testing.T) {
	report := Validate([]byte(`{"name":"x","workflows":[
      {"workflow_id":"x.main","entry_step_id":"g","steps":[
        {"step_id":"g","type":"parallel","branches":["x.nowhere"]}]}]}`), stdlib.Default())
	if report.OK() {
		t.Fatal("a branch naming no workflow must be an error")
	}
	if !strings.Contains(report.String(), "x.nowhere") {
		t.Errorf("report %q does not name the dangling branch", report.String())
	}
}

func TestValidateCatchesACycleThroughABranch(t *testing.T) {
	report := Validate([]byte(`{"name":"x","workflows":[
      {"workflow_id":"x.main","entry_step_id":"g","steps":[
        {"step_id":"g","type":"parallel","branches":["x.a"]}]},
      {"workflow_id":"x.a","entry_step_id":"back","steps":[
        {"step_id":"back","type":"composite","sub_workflow_ref":"x.main"}]}]}`), stdlib.Default())
	if report.OK() {
		t.Fatal("a branch pointing back at its own parent must be caught before runtime")
	}
	if !strings.Contains(report.String(), "cycle") {
		t.Errorf("report %q does not identify the cycle", report.String())
	}
}

func TestValidateWarnsOnASingleBranch(t *testing.T) {
	report := Validate([]byte(`{"name":"x","workflows":[
      {"workflow_id":"x.main","entry_step_id":"g","steps":[
        {"step_id":"g","type":"parallel","branches":["x.a"]}]},
      {"workflow_id":"x.a","entry_step_id":"a","steps":[
        {"step_id":"a","type":"leaf","action_ref":"std.set","with":{"fields":{"n":1}}}]}]}`),
		stdlib.Default())
	if !report.OK() {
		t.Fatalf("one branch is legal, just pointless: %v", report)
	}
	if !strings.Contains(report.String(), "one branch") {
		t.Errorf("report %q does not warn about the single branch", report.String())
	}
}

func TestTheShippedParallelExampleValidatesAndRuns(t *testing.T) {
	data, err := os.ReadFile("../examples/manifests/onboarding-checks.json")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if report := Validate(data, stdlib.Default()); !report.OK() {
		t.Fatalf("the shipped example must validate clean: %v", report)
	}

	cases := []struct {
		name    string
		ctx     map[string]any
		outcome string
	}{
		{"clean applicant", map[string]any{
			"credit_score": 780, "country": "GB", "address_age_months": 24}, "approved"},
		{"sanctioned country", map[string]any{
			"credit_score": 780, "country": "KP", "address_age_months": 24}, "declined"},
		{"thin credit file", map[string]any{
			"credit_score": 500, "country": "GB", "address_age_months": 24}, "manual_review"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			output, err := loadAndRun(t, string(data), "onboarding.screen", tc.ctx)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if output["outcome"] != tc.outcome {
				t.Errorf("outcome = %v, want %v", output["outcome"], tc.outcome)
			}
			for _, field := range []string{"credit_ok", "sanctions_hit", "address_stable"} {
				if _, ok := output[field]; !ok {
					t.Errorf("branch result %q missing from the joined context", field)
				}
			}
		})
	}
}
