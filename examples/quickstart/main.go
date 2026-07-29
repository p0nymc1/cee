// Command quickstart is the smallest useful integration: what adopting CEE
// actually looks like from inside a service you already have.
//
// CEE is a library, not a server. Nothing here listens on a port. Your own
// HTTP handler or queue consumer calls engine.Run and gets a result back --
// the engine is a state machine living inside your process, not something you
// deploy alongside it.
//
// The flow below is a refund desk, chosen because it has the property that
// makes CEE worth the trouble: an irreversible action. Small refunds settle
// themselves, large ones wait for a manager, and no refund is ever attempted
// against a closed account.
//
// The three behaviours are worth separating, because only one of them is
// ordinary control flow:
//
//   - paying out is a step succeeding;
//   - waiting for a manager is a suspension -- not a failure, so no breaker
//     sees it, and the run can be picked up later from its pointer;
//   - a closed account is caught by a sandbox probe BEFORE the payout runs,
//     so the money never moves and an operator is told why.
//
// This file is compiled and tested with the rest of the repository, so the
// snippet in the README cannot rot into something that no longer builds.
//
// Run it with `go run ./examples/quickstart`.
package main

import (
	"fmt"

	"github.com/p0nymc1/cee/entities"
	"github.com/p0nymc1/cee/execution"
	"github.com/p0nymc1/cee/intentrouter"
	"github.com/p0nymc1/cee/registry"
	"github.com/p0nymc1/cee/sandbox"
)

// Your own data. The engine never sees it -- only the probe reads it.
var closedAccounts = map[string]bool{"acct-991": true}

const managerApprovalOver = 100.0

func buildRuntime() (*intentrouter.Router, *execution.Engine) {
	router := intentrouter.NewRouter(0.34)
	sb := sandbox.NewSandbox()
	engine := execution.NewEngine(sb)
	engine.SetStore(execution.NewMemoryStore()) // filestore.New(dir) to survive a restart

	// The guardrail. Read-only, and it runs before the payout, not after.
	sb.RegisterProbe("refund.account_open", func(ctx map[string]any) (bool, string, error) {
		account, _ := ctx["account"].(string)
		if closedAccounts[account] {
			return false, fmt.Sprintf("account %s is closed; the refund would bounce", account), nil
		}
		return true, "", nil
	})

	registry.NewRegistry(router, engine).RegisterDomain(registry.Domain{
		Name: "refunds",
		Intents: []entities.IntentNode{{
			NodeID:           "refunds.request",
			DomainID:         "refunds",
			Examples:         []string{"customer wants a refund", "please refund this order"},
			EntryWorkflowRef: "refunds.process",
		}},
		Policies: []execution.CircuitBreakerPolicy{
			{PolicyID: "needs_human", FallbackStepRef: "hold"},
		},
		Workflows: []*execution.Workflow{{
			WorkflowID:  "refunds.process",
			EntryStepID: "pay",
			Steps: map[string]execution.Step{
				"pay": &execution.LeafStep{
					StepID:                  "pay",
					SandboxProbeRef:         "refund.account_open",
					CircuitBreakerPolicyRef: "needs_human",
					Run: func(ctx map[string]any) (map[string]any, error) {
						amount, _ := ctx["amount"].(float64)
						if amount > managerApprovalOver {
							return execution.Suspend("refund over the limit needs a manager")
						}
						return map[string]any{"paid": true}, nil
					},
				},
				"hold": &execution.LeafStep{
					StepID: "hold",
					Run: func(ctx map[string]any) (map[string]any, error) {
						return map[string]any{"paid": false}, nil
					},
				},
			},
		}},
	})
	return router, engine
}

// handle is the shape your HTTP handler or queue consumer would have.
func handle(engine *execution.Engine, entry string, account string, amount float64) string {
	result, err := engine.Run(entry, map[string]any{"account": account, "amount": amount})
	switch {
	case err != nil:
		return fmt.Sprintf("halted: %v", err)
	case result.StatePointer != "":
		// Parked. Hand the pointer to whatever collects the decision, then
		// engine.Resume(pointer, map[string]any{"approved": true}).
		return fmt.Sprintf("parked for a manager (pointer %s…)", result.StatePointer[:6])
	case result.Output["paid"] == true:
		return "paid"
	default:
		return fmt.Sprintf("held: %v", result.Output[execution.FailureReasonKey])
	}
}

func main() {
	router, engine := buildRuntime()

	match := router.Match("refunds", "customer wants a refund")
	if !match.Matched {
		fmt.Println("no intent matched")
		return
	}

	for _, c := range []struct {
		account string
		amount  float64
	}{
		{"acct-100", 20},  // ordinary
		{"acct-100", 500}, // over the limit
		{"acct-991", 20},  // closed account
	} {
		fmt.Printf("%-9s $%-6.0f -> %s\n", c.account, c.amount,
			handle(engine, match.EntryWorkflowRef, c.account, c.amount))
	}
}
