package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/p0nymc1/cee/execution"
	"github.com/p0nymc1/cee/intentrouter"
	"github.com/p0nymc1/cee/manifest"
	"github.com/p0nymc1/cee/registry"
	"github.com/p0nymc1/cee/replay"
	"github.com/p0nymc1/cee/sandbox"
	"github.com/p0nymc1/cee/stdlib"
)

const manifestTemplate = `{
  "name": "refunds",
  "policies": [{"policy_id": "needs_human", "fallback_step_ref": "hold"}],
  "workflows": [{
    "workflow_id": "refunds.process",
    "entry_step_id": "under_limit",
    "steps": [
      {"step_id": "under_limit", "type": "leaf", "action_ref": "std.require",
       "with": {"field": "amount", "op": "lte", "value": %s},
       "sandbox_probe_ref": "refund.account_open",
       "circuit_breaker_policy_ref": "needs_human", "on_success": "pay"},
      {"step_id": "pay", "type": "leaf", "action_ref": "std.set",
       "with": {"fields": {"paid": true}}},
      {"step_id": "hold", "type": "leaf", "action_ref": "std.set",
       "with": {"fields": {"paid": false}}}
    ]
  }]
}`

type refund struct {
	ID      string
	Account string
	Amount  float64
}

var closedAccounts = map[string]bool{"acct-0019": true, "acct-0031": true}

func history() []refund {
	amounts := []float64{
		12, 45, 8, 220, 63, 95, 30, 150, 74, 19,
		88, 41, 5, 310, 57, 99, 26, 68, 130, 52,
		7, 85, 38, 240, 61, 91, 15, 72, 105, 49,
		80, 23, 66, 190, 55, 97, 34,
	}
	out := make([]refund, 0, len(amounts))
	for i, amount := range amounts {
		out = append(out, refund{
			ID:      fmt.Sprintf("r-%04d", i+1),
			Account: fmt.Sprintf("acct-%04d", i+1),
			Amount:  amount,
		})
	}
	return out
}

func buildEngine(limit string, prober execution.Prober) (*execution.Engine, error) {
	domain, err := manifest.Load([]byte(fmt.Sprintf(manifestTemplate, limit)), nil, stdlib.Default())
	if err != nil {
		return nil, err
	}
	engine := execution.NewEngine(prober)
	registry.NewRegistry(intentrouter.NewRouter(0.5), engine).RegisterDomain(*domain)
	return engine, nil
}

func liveSandbox() *sandbox.Sandbox {
	sb := sandbox.NewSandbox()
	sb.RegisterProbe("refund.account_open", func(ctx map[string]any) (bool, string, error) {
		account, _ := ctx["account"].(string)
		if closedAccounts[account] {
			return false, fmt.Sprintf("account %s is closed; the refund would bounce", account), nil
		}
		return true, "", nil
	})
	return sb
}

func record(limit string) ([]replay.Recording, error) {
	recordings := make([]replay.Recording, 0, len(history()))
	for _, r := range history() {
		recorder := replay.NewRecorder(liveSandbox())
		engine, err := buildEngine(limit, recorder)
		if err != nil {
			return nil, err
		}
		input := map[string]any{"refund_id": r.ID, "account": r.Account, "amount": r.Amount}
		result, runErr := engine.Run("refunds.process", input)
		recordings = append(recordings, recorder.Finish("refunds.process", input, result, runErr))
	}
	return recordings, nil
}

type change struct {
	RefundID    string
	Amount      float64
	Before      string
	After       string
	Flipped     bool
	Differences []replay.Difference
}

func decisionIn(output map[string]any) string {
	if paid, _ := output["paid"].(bool); paid {
		return "paid"
	}
	return "held"
}

func replayUnder(limit string, recordings []replay.Recording) ([]change, error) {
	var changes []change
	for _, rec := range recordings {
		player := replay.NewPlayer(rec)
		engine, err := buildEngine(limit, player)
		if err != nil {
			return nil, err
		}
		result, runErr := engine.Run(rec.WorkflowID, rec.Input)
		diffs := replay.Compare(rec, result, runErr)
		if len(diffs) == 0 {
			continue
		}
		amount, _ := rec.Input["amount"].(float64)
		id, _ := rec.Input["refund_id"].(string)
		before, after := decisionIn(rec.Output), decisionIn(result.Output)
		changes = append(changes, change{
			RefundID: id, Amount: amount,
			Before: before, After: after,
			Flipped:     before != after,
			Differences: diffs,
		})
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].RefundID < changes[j].RefundID })
	return changes, nil
}

func outcome(rec replay.Recording) string {
	if paid, _ := rec.Output["paid"].(bool); paid {
		return "paid"
	}
	return "held"
}

func run(before, after string) (string, error) {
	recordings, err := record(before)
	if err != nil {
		return "", err
	}

	paid := 0
	for _, rec := range recordings {
		if outcome(rec) == "paid" {
			paid++
		}
	}

	changes, err := replayUnder(after, recordings)
	if err != nil {
		return "", err
	}

	var flipped, explanationOnly []change
	for _, c := range changes {
		if c.Flipped {
			flipped = append(flipped, c)
		} else {
			explanationOnly = append(explanationOnly, c)
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Last quarter, under the rule in force (auto-approve at or under $%s):\n", before)
	fmt.Fprintf(&b, "  %d refunds -- %d paid, %d held\n\n", len(recordings), paid, len(recordings)-paid)

	fmt.Fprintf(&b, "Proposed change: tighten the limit to $%s.\n", after)
	fmt.Fprintf(&b, "Replaying the same %d refunds against the new rule:\n\n", len(recordings))
	fmt.Fprintf(&b, "  %d of %d decisions flip\n\n", len(flipped), len(recordings))

	for _, c := range flipped {
		fmt.Fprintf(&b, "    %-8s $%-6.0f %s -> %s\n", c.RefundID, c.Amount, c.Before, c.After)
	}

	if len(explanationOnly) > 0 {
		fmt.Fprintf(&b, "\n  %d more were held before and are held now. The outcome stands; only the\n", len(explanationOnly))
		b.WriteString("  reason given to the operator changes, for example:\n\n")
		sample := explanationOnly[0]
		for _, d := range sample.Differences {
			fmt.Fprintf(&b, "    %-8s %s\n", sample.RefundID, d)
		}
	}

	b.WriteString("\nOne refund did not flip that the amounts alone say should have. The probe\n")
	b.WriteString("verdicts come from the recording rather than a live check, so an account that\n")
	b.WriteString("was closed at the time stays closed, and the rule is the only thing that moved.\n")
	return b.String(), nil
}

func main() {
	report, err := run("100", "50")
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	fmt.Print(report)
}
