package manifest

import (
	"strings"
	"testing"

	"cee/execution"
	"cee/intentrouter"
	"cee/registry"
	"cee/stdlib"
)

// A complete no-code plugin: every action is a standard-library action,
// there are zero Go hooks. This is the L1 contribution tier -- a manifest a
// non-Go author could publish. amount <= threshold passes through to
// "approve"; otherwise the require step fails and the breaker routes to
// "flag".
const noCodeManifestJSON = `
{
  "name": "expense-guard",
  "intents": [
    {"node_id": "expense-guard.screen", "examples": ["screen this expense"], "entry_step_ref": "expense-guard.screen_expense"}
  ],
  "policies": [
    {"policy_id": "route_to_flag", "fallback_step_ref": "flag"}
  ],
  "workflows": [{
    "workflow_id": "expense-guard.screen_expense",
    "entry_step_id": "check_threshold",
    "steps": [
      {"step_id": "check_threshold", "type": "leaf", "action_ref": "std.require",
       "with": {"field": "amount", "op": "lte", "value": 10000},
       "circuit_breaker_policy_ref": "route_to_flag", "on_success": "approve"},
      {"step_id": "approve", "type": "leaf", "action_ref": "std.set", "with": {"fields": {"approved": true}}},
      {"step_id": "flag", "type": "leaf", "action_ref": "std.set", "with": {"fields": {"flagged": true}}}
    ]
  }]
}`

func TestNoCodeManifestValidatesClean(t *testing.T) {
	report := Validate([]byte(noCodeManifestJSON), stdlib.Default())
	if !report.OK() {
		t.Fatalf("expected a clean report, got:\n%s", report.String())
	}
	for _, issue := range report.Issues {
		if issue.Severity == Warning {
			t.Fatalf("a pure standard-library manifest should have no warnings, got: %s", issue.Message)
		}
	}
}

func TestNoCodeManifestRunsWithNoHooks(t *testing.T) {
	domain, err := Load([]byte(noCodeManifestJSON), nil, stdlib.Default())
	if err != nil {
		t.Fatalf("unexpected load error: %v", err)
	}
	router := intentrouter.NewRouter(0.3)
	engine := execution.NewEngine(nil)
	registry.NewRegistry(router, engine).RegisterDomain(*domain)

	approved, err := engine.Run("expense-guard.screen_expense", map[string]any{"amount": 500.0})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if approved.Output["approved"] != true {
		t.Fatalf("expected small expense approved, got %+v", approved.Output)
	}

	flagged, err := engine.Run("expense-guard.screen_expense", map[string]any{"amount": 50000.0})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if flagged.Output["flagged"] != true {
		t.Fatalf("expected large expense flagged via circuit breaker, got %+v", flagged.Output)
	}
}

func TestValidateCatchesDanglingOnSuccess(t *testing.T) {
	bad := `{
		"name": "broken",
		"workflows": [{
			"workflow_id": "broken.wf",
			"entry_step_id": "a",
			"steps": [{"step_id": "a", "type": "leaf", "action_ref": "std.set", "with": {"fields": {}}, "on_success": "nowhere"}]
		}]
	}`
	report := Validate([]byte(bad), stdlib.Default())
	if report.OK() {
		t.Fatalf("expected an error for a dangling on_success")
	}
	if !strings.Contains(report.String(), "nowhere") {
		t.Fatalf("expected the dangling target to be named, got:\n%s", report.String())
	}
}

func TestValidateCatchesUndeclaredPolicy(t *testing.T) {
	bad := `{
		"name": "broken",
		"workflows": [{
			"workflow_id": "broken.wf",
			"entry_step_id": "a",
			"steps": [{"step_id": "a", "type": "leaf", "action_ref": "std.set", "with": {"fields": {}}, "circuit_breaker_policy_ref": "ghost"}]
		}]
	}`
	report := Validate([]byte(bad), stdlib.Default())
	if report.OK() {
		t.Fatalf("expected an error for an undeclared circuit_breaker_policy_ref")
	}
}

func TestValidateCatchesMisconfiguredStandardAction(t *testing.T) {
	bad := `{
		"name": "broken",
		"workflows": [{
			"workflow_id": "broken.wf",
			"entry_step_id": "a",
			"steps": [{"step_id": "a", "type": "leaf", "action_ref": "std.require", "with": {"field": "x", "op": "bogus", "value": 1}}]
		}]
	}`
	report := Validate([]byte(bad), stdlib.Default())
	if report.OK() {
		t.Fatalf("expected an error for a misconfigured std.require")
	}
}

func TestValidateWarnsOnCustomHookAction(t *testing.T) {
	m := `{
		"name": "finance",
		"workflows": [{
			"workflow_id": "finance.wf",
			"entry_step_id": "a",
			"steps": [{"step_id": "a", "type": "leaf", "action_ref": "finance.custom_thing"}]
		}]
	}`
	report := Validate([]byte(m), stdlib.Default())
	if !report.OK() {
		t.Fatalf("a custom hook ref is not an error at validate time, got:\n%s", report.String())
	}
	foundWarning := false
	for _, issue := range report.Issues {
		if issue.Severity == Warning && strings.Contains(issue.Message, "finance.custom_thing") {
			foundWarning = true
		}
	}
	if !foundWarning {
		t.Fatalf("expected a warning that the custom action is only checked at load time")
	}
}
