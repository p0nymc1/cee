package main

import (
	"fmt"

	"github.com/p0nymc1/cee/diagnostics"
	"github.com/p0nymc1/cee/entities"
	"github.com/p0nymc1/cee/execution"
	"github.com/p0nymc1/cee/intentrouter"
	"github.com/p0nymc1/cee/registry"
	"github.com/p0nymc1/cee/sandbox"
	"github.com/p0nymc1/cee/scorecard"
)

var criticalAssets = map[string]bool{
	"dc01":     true,
	"coredb01": true,
}

func buildRuntime() (*intentrouter.Router, *execution.Engine) {
	router := intentrouter.NewRouter(0.34)
	sb := sandbox.NewSandbox()
	engine := execution.NewEngine(sb)
	reg := registry.NewRegistry(router, engine)

	sb.RegisterProbe("security.assess_containment_impact", func(ctx map[string]any) (bool, string, error) {
		host, _ := ctx["target_host"].(string)
		if criticalAssets[host] {
			return false, fmt.Sprintf("containment would isolate critical asset %q", host), nil
		}
		return true, "", nil
	})

	reg.RegisterDomain(securityDomain())

	return router, engine
}

func main() {
	router, engine := buildRuntime()

	fmt.Println("== Scenario 1: brute-force against an ordinary workstation ==")
	runEvent(router, engine, "repeated failed login attempts spike from one source", "ws-4471")

	fmt.Println()
	fmt.Println("== Scenario 2: the same technique, but against a domain controller ==")
	runEvent(router, engine, "repeated failed login attempts spike from one source", "dc01")

	fmt.Println()
	fmt.Println("== Aggregate diagnostics across a batch ==")
	fmt.Printf("  %s\n", runBatch())
}

// runBatch shows the error side of the picture the scorecard leaves out. The
// per-event scorecards above report what CEE did efficiently; a diagnostics
// recorder aggregates what went wrong across a batch -- how often routing
// missed, how often the sandbox refused a step. It attaches once, for the whole
// batch, rather than per event.
func runBatch() diagnostics.Report {
	rec := diagnostics.NewRecorder()

	router, engine := buildRuntime()
	router.SetObserver(rec)
	engine.SetObserver(rec)

	events := []struct{ alert, host string }{
		{"repeated failed login attempts spike from one source", "ws-4471"},
		{"many failed logins from one source", "ws-8800"},
		{"password spray detected", "dc01"}, // probe refuses: critical asset
		{"login from unusual location for known account", "ws-1200"},
		{"nightly database backup completed successfully", "ws-1200"}, // no technique matches
	}

	for _, e := range events {
		rec.ObserveRun()
		match := router.Match("security", e.alert)
		if !match.Matched {
			continue
		}
		engine.Run(match.EntryWorkflowRef, map[string]any{
			"target_host": e.host,
			"technique":   match.NodeRef,
		})
	}

	return rec.Report()
}

func runEvent(router *intentrouter.Router, engine *execution.Engine, alertText, targetHost string) {
	match := router.Match("security", alertText)
	if !match.Matched {
		fmt.Printf("  no ATT&CK technique matched (confidence %.2f) -> would fall through to edge LLM extraction\n", match.Confidence)
		return
	}
	fmt.Printf("  matched technique %s (confidence %.2f) -> entering workflow %s\n",
		match.NodeRef, match.Confidence, match.EntryWorkflowRef)

	recorder := scorecard.NewRecorder()
	engine.SetObserver(recorder)

	result, err := engine.Run(match.EntryWorkflowRef, map[string]any{
		"target_host": targetHost,
		"technique":   match.NodeRef,
	})
	if err != nil {
		fmt.Printf("  workflow halted: %v\n", err)
		return
	}
	fmt.Printf("  outcome: %s\n", describe(result.Output))
	fmt.Printf("  trace:   %v\n", result.Trace)
	fmt.Printf("  %s\n", recorder.Snapshot(match.EntryWorkflowRef))
}

func describe(output map[string]any) string {
	switch {
	case output["contained"] == true:
		return "threat auto-contained (host isolated by deterministic action)"
	case output["awaiting_human_approval"] == true:
		return "containment held for human approval (breaker downgraded, critical asset protected)"
	default:
		return fmt.Sprintf("%v", output)
	}
}

func securityDomain() registry.Domain {
	return registry.Domain{
		Name: "security",
		Intents: []entities.IntentNode{
			{
				NodeID:           "security.T1110_brute_force",
				DomainID:         "security",
				Examples:         []string{"repeated failed login attempts spike", "many failed logins from one source", "password spray detected"},
				EntryWorkflowRef: "security.contain_threat",
			},
			{
				NodeID:           "security.T1078_valid_account_abuse",
				DomainID:         "security",
				Examples:         []string{"login from unusual location for known account", "valid credentials used at impossible travel speed"},
				EntryWorkflowRef: "security.contain_threat",
			},
		},
		Policies: []execution.CircuitBreakerPolicy{

			{PolicyID: "security_containment_gate", FallbackStepRef: "hold_for_human_approval"},
		},
		Workflows: []*execution.Workflow{
			{
				WorkflowID:  "security.contain_threat",
				EntryStepID: "classify",
				Steps: map[string]execution.Step{
					"classify": &execution.LeafStep{
						StepID: "classify",
						Run: func(ctx map[string]any) (map[string]any, error) {

							return map[string]any{"sop": "isolate_host"}, nil
						},
						OnSuccess: "contain",
					},
					"contain": &execution.LeafStep{
						StepID:                  "contain",
						SandboxProbeRef:         "security.assess_containment_impact",
						CircuitBreakerPolicyRef: "security_containment_gate",
						Run: func(ctx map[string]any) (map[string]any, error) {

							return map[string]any{"contained": true}, nil
						},
					},
					"hold_for_human_approval": &execution.LeafStep{
						StepID: "hold_for_human_approval",
						Run: func(ctx map[string]any) (map[string]any, error) {
							return map[string]any{"awaiting_human_approval": true}, nil
						},
					},
				},
			},
		},
	}
}
