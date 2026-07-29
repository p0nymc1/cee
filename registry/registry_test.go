package registry

// This test is CEE's "Phase 0" stress test in miniature: two domains that
// share no vocabulary or code register into the same Router and Engine, and
// neither the router nor the engine needs to change to support either one.

import (
	"testing"

	"cee/entities"
	"cee/execution"
	"cee/intentrouter"
)

func TestTwoUnrelatedDomainsCoexistWithoutEngineChanges(t *testing.T) {
	router := intentrouter.NewRouter(0.5)
	engine := execution.NewEngine(nil)
	reg := NewRegistry(router, engine)

	reg.RegisterDomain(Domain{
		Name: "finance",
		Intents: []entities.IntentNode{{
			NodeID:           "finance.duplicate_expense",
			DomainID:         "finance",
			Examples:         []string{"duplicate expense report"},
			EntryWorkflowRef: "finance.flag",
		}},
		Workflows: []*execution.Workflow{{
			WorkflowID:  "finance.flag",
			EntryStepID: "flag",
			Steps: map[string]execution.Step{
				"flag": &execution.LeafStep{
					StepID: "flag",
					Run: func(ctx map[string]any) (map[string]any, error) {
						return map[string]any{"flagged": true}, nil
					},
				},
			},
		}},
	})

	reg.RegisterDomain(Domain{
		Name: "security",
		Intents: []entities.IntentNode{{
			NodeID:           "security.suspicious_login",
			DomainID:         "security",
			Examples:         []string{"suspicious login from unusual location"},
			EntryWorkflowRef: "security.contain",
		}},
		Workflows: []*execution.Workflow{{
			WorkflowID:  "security.contain",
			EntryStepID: "contain",
			Steps: map[string]execution.Step{
				"contain": &execution.LeafStep{
					StepID: "contain",
					Run: func(ctx map[string]any) (map[string]any, error) {
						return map[string]any{"contained": true}, nil
					},
				},
			},
		}},
	})

	financeMatch := router.Match("finance", "duplicate expense report")
	securityMatch := router.Match("security", "suspicious login from unusual location")

	if !financeMatch.Matched || !securityMatch.Matched {
		t.Fatalf("expected both domains to match: finance=%+v security=%+v", financeMatch, securityMatch)
	}

	financeResult, err := engine.Run(financeMatch.EntryWorkflowRef, map[string]any{})
	if err != nil {
		t.Fatalf("finance workflow failed: %v", err)
	}
	securityResult, err := engine.Run(securityMatch.EntryWorkflowRef, map[string]any{})
	if err != nil {
		t.Fatalf("security workflow failed: %v", err)
	}

	if financeResult.Output["flagged"] != true || securityResult.Output["contained"] != true {
		t.Fatalf("unexpected outputs: finance=%+v security=%+v", financeResult.Output, securityResult.Output)
	}

	domains := reg.Domains()
	if len(domains) != 2 {
		t.Fatalf("expected 2 registered domains, got %v", domains)
	}
}

func TestRegisterDomainStampsWorkflowsWithTheirDomain(t *testing.T) {
	workflow := &execution.Workflow{
		WorkflowID:  "security.contain_threat",
		EntryStepID: "contain",
		Steps: map[string]execution.Step{
			"contain": &execution.LeafStep{
				StepID: "contain",
				Run: func(ctx map[string]any) (map[string]any, error) {
					return map[string]any{"contained": true}, nil
				},
			},
		},
	}

	reg := NewRegistry(intentrouter.NewRouter(0.5), execution.NewEngine(nil))
	reg.RegisterDomain(Domain{Name: "security", Workflows: []*execution.Workflow{workflow}})

	// The domain author never wrote DomainID: registering is what binds a
	// workflow to its domain, so the two can never disagree.
	if workflow.DomainID != "security" {
		t.Fatalf("expected registration to stamp DomainID=security, got %q", workflow.DomainID)
	}
}
