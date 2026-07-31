package main

import (
	"fmt"

	"github.com/p0nymc1/cee/entities"
	"github.com/p0nymc1/cee/execution"
	"github.com/p0nymc1/cee/intentrouter"
	"github.com/p0nymc1/cee/registry"
	"github.com/p0nymc1/cee/sandbox"
)

var closedAccounts = map[string]bool{"acct-991": true}

const managerApprovalOver = 100.0

func buildRuntime() (*intentrouter.Router, *execution.Engine) {
	router := intentrouter.NewRouter(0.34)
	sb := sandbox.NewSandbox()
	engine := execution.NewEngine(sb)
	engine.SetStore(execution.NewMemoryStore())

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

func handle(engine *execution.Engine, entry string, account string, amount float64) string {
	result, err := engine.Run(entry, map[string]any{"account": account, "amount": amount})
	switch {
	case err != nil:
		return fmt.Sprintf("halted: %v", err)
	case result.StatePointer != "":

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
		{"acct-100", 20},
		{"acct-100", 500},
		{"acct-991", 20},
	} {
		fmt.Printf("%-9s $%-6.0f -> %s\n", c.account, c.amount,
			handle(engine, match.EntryWorkflowRef, c.account, c.amount))
	}
}
