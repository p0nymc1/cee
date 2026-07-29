// Command security_monitoring is a runnable, end-to-end demonstration of the
// CEE monitoring-class scenario designed on the whiteboard: intent routing
// against MITRE ATT&CK-style techniques, a pre-execution sandbox gate in
// front of the containment action, and a circuit breaker that DOWNGRADES to
// human approval rather than auto-switching -- the containment-specific
// breaker semantics that make this scenario distinct from anomaly detection.
//
// It is deliberately a domain plugin: it touches only the public APIs of
// intentrouter / execution / sandbox / registry, never the engine internals.
// Run it with `go run ./examples/security_monitoring`.
package main

import (
	"fmt"

	"cee/entities"
	"cee/execution"
	"cee/intentrouter"
	"cee/registry"
	"cee/sandbox"
	"cee/scorecard"
)

// criticalAssets is the security domain's own data -- the engine knows
// nothing about it. The sandbox probe consults it to decide whether a
// containment action would hit something too important to touch
// automatically.
var criticalAssets = map[string]bool{
	"dc01":     true, // domain controller
	"coredb01": true, // core database
}

// buildRuntime wires up the shared runtime and registers the security
// domain plugin. Both main and the package test go through it, so the demo
// and its test can never drift apart.
func buildRuntime() (*intentrouter.Router, *execution.Engine) {
	router := intentrouter.NewRouter(0.34)
	sb := sandbox.NewSandbox()
	engine := execution.NewEngine(sb)
	reg := registry.NewRegistry(router, engine)

	// the security domain registers its probe (read-only simulation)
	sb.RegisterProbe("security.assess_containment_impact", func(ctx map[string]any) (bool, string, error) {
		host, _ := ctx["target_host"].(string)
		if criticalAssets[host] {
			return false, fmt.Sprintf("containment would isolate critical asset %q", host), nil
		}
		return true, "", nil
	})

	// the security domain registers its plugin (intents + DAG + policy)
	reg.RegisterDomain(securityDomain())

	return router, engine
}

func main() {
	router, engine := buildRuntime()

	// --- drive two events through the same workflow ---
	fmt.Println("== Scenario 1: brute-force against an ordinary workstation ==")
	runEvent(router, engine, "repeated failed login attempts spike from one source", "ws-4471")

	fmt.Println()
	fmt.Println("== Scenario 2: the same technique, but against a domain controller ==")
	runEvent(router, engine, "repeated failed login attempts spike from one source", "dc01")
}

func runEvent(router *intentrouter.Router, engine *execution.Engine, alertText, targetHost string) {
	match := router.Match("security", alertText)
	if !match.Matched {
		fmt.Printf("  no ATT&CK technique matched (confidence %.2f) -> would fall through to edge LLM extraction\n", match.Confidence)
		return
	}
	fmt.Printf("  matched technique %s (confidence %.2f) -> entering workflow %s\n",
		match.NodeRef, match.Confidence, match.EntryWorkflowRef)

	// Measure this request. The recorder is per-request; attaching it to the
	// engine costs nothing when no request is in flight because Run only
	// calls the observer while it is set.
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

// securityDomain builds the monitoring-class plugin. The DAG:
//
//	classify -> [sandbox gate] contain
//	                 |fail-> hold_for_human_approval
//
// classify maps the matched technique to a containment SOP (deterministic,
// no LLM). contain is gated by the sandbox probe; if the probe reports the
// target is a critical asset, the breaker routes to hold_for_human_approval
// instead of auto-executing isolation.
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
			// Containment-specific breaker: on failure, do NOT auto-switch to
			// an alternate path -- hold for a human. This is the security
			// scenario's distinct breaker semantics.
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
							// Deterministic: map technique -> containment SOP.
							return map[string]any{"sop": "isolate_host"}, nil
						},
						OnSuccess: "contain",
					},
					"contain": &execution.LeafStep{
						StepID:                  "contain",
						SandboxProbeRef:         "security.assess_containment_impact",
						CircuitBreakerPolicyRef: "security_containment_gate",
						Run: func(ctx map[string]any) (map[string]any, error) {
							// Only reached if the sandbox probe deemed it safe.
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
