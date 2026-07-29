package manifest

import (
	"fmt"
	"testing"

	"cee/execution"
	"cee/intentrouter"
	"cee/registry"
	"cee/stdlib"
)

const financeManifestJSON = `
{
  "name": "finance",
  "intents": [
    {
      "node_id": "finance.duplicate_expense",
      "examples": ["duplicate expense report", "same receipt submitted twice"],
      "entry_step_ref": "finance.flag_duplicate"
    }
  ],
  "policies": [
    {"policy_id": "escalate_to_review", "fallback_step_ref": "human_review"}
  ],
  "workflows": [
    {
      "workflow_id": "finance.flag_duplicate",
      "entry_step_id": "check",
      "steps": [
        {
          "step_id": "check",
          "type": "leaf",
          "action_ref": "finance.check_duplicate",
          "circuit_breaker_policy_ref": "escalate_to_review",
          "on_success": "notify"
        },
        {
          "step_id": "notify",
          "type": "leaf",
          "action_ref": "finance.notify_finance_team"
        },
        {
          "step_id": "human_review",
          "type": "leaf",
          "action_ref": "finance.queue_human_review"
        }
      ]
    }
  ]
}`

func financeHooks() Hooks {
	return Hooks{
		"finance.check_duplicate": func(ctx map[string]any) (map[string]any, error) {
			return map[string]any{"duplicate": true}, nil
		},
		"finance.notify_finance_team": func(ctx map[string]any) (map[string]any, error) {
			return map[string]any{"notified": true}, nil
		},
		"finance.queue_human_review": func(ctx map[string]any) (map[string]any, error) {
			return map[string]any{"escalated": true}, nil
		},
	}
}

func TestLoadBuildsRunnableWorkflow(t *testing.T) {
	domain, err := Load([]byte(financeManifestJSON), financeHooks(), stdlib.Default())
	if err != nil {
		t.Fatalf("unexpected error loading manifest: %v", err)
	}

	router := intentrouter.NewRouter(0.5)
	engine := execution.NewEngine(nil)
	reg := registry.NewRegistry(router, engine)
	reg.RegisterDomain(*domain)

	match := router.Match("finance", "duplicate expense report submitted again")
	if !match.Matched {
		t.Fatalf("expected intent match, got %+v", match)
	}

	result, err := engine.Run(match.EntryStepRef, map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error running workflow: %v", err)
	}
	if result.Output["notified"] != true {
		t.Fatalf("expected notified=true, got %+v", result.Output)
	}
}

func TestLoadFallsBackThroughManifestDeclaredPolicy(t *testing.T) {
	hooks := financeHooks()
	hooks["finance.check_duplicate"] = func(ctx map[string]any) (map[string]any, error) {
		return nil, fmt.Errorf("erp lookup failed")
	}

	domain, err := Load([]byte(financeManifestJSON), hooks, stdlib.Default())
	if err != nil {
		t.Fatalf("unexpected error loading manifest: %v", err)
	}

	router := intentrouter.NewRouter(0.5)
	engine := execution.NewEngine(nil)
	reg := registry.NewRegistry(router, engine)
	reg.RegisterDomain(*domain)

	result, err := engine.Run("finance.flag_duplicate", map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error running workflow: %v", err)
	}
	if result.Output["escalated"] != true {
		t.Fatalf("expected the manifest's circuit_breaker_policy_ref to route to human_review, got %+v", result.Output)
	}
}

func TestLoadFailsOnUnregisteredActionRef(t *testing.T) {
	_, err := Load([]byte(financeManifestJSON), Hooks{}, stdlib.Default())
	if err == nil {
		t.Fatalf("expected an error for unregistered action_ref")
	}
}

func TestLoadFailsOnInvalidJSON(t *testing.T) {
	_, err := Load([]byte("not json"), Hooks{}, stdlib.Default())
	if err == nil {
		t.Fatalf("expected an error for invalid JSON")
	}
}

func TestLoadFailsOnCompositeStepMissingSubWorkflowRef(t *testing.T) {
	badJSON := `{
		"name": "broken",
		"workflows": [{
			"workflow_id": "broken.wf",
			"entry_step_id": "step1",
			"steps": [{"step_id": "step1", "type": "composite"}]
		}]
	}`
	_, err := Load([]byte(badJSON), Hooks{}, stdlib.Default())
	if err == nil {
		t.Fatalf("expected an error for composite step missing sub_workflow_ref")
	}
}
