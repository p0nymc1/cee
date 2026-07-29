// Command human_approval is a runnable, end-to-end demonstration of a
// workflow that pauses for a human and is later picked back up.
//
// Two things make it worth reading together. First, it is a pure L1 plugin:
// the whole DAG is the JSON manifest at examples/manifests/expense-approval.json
// and there is not one Go hook -- every step is a standard action. Second, it
// exercises suspend/resume, so the "hold this for a person" branch actually
// has a second half instead of dead-ending at a flag in the output.
//
// Run it with `go run ./examples/human_approval`.
package main

import (
	"fmt"
	"os"

	"cee/entities"
	"cee/execution"
	"cee/intentrouter"
	"cee/manifest"
	"cee/registry"
	"cee/stdlib"
)

const manifestPath = "examples/manifests/expense-approval.json"

// buildRuntime wires the shared runtime and loads the domain from its
// manifest. Both main and the package test go through it, so the demo and
// its test cannot drift apart.
func buildRuntime() (*intentrouter.Router, *execution.Engine, *execution.MemoryStore, error) {
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("reading %s: %w", manifestPath, err)
	}

	// No hooks: nil. Every action_ref in this manifest is a standard action.
	domain, err := manifest.Load(data, nil, stdlib.Default())
	if err != nil {
		return nil, nil, nil, fmt.Errorf("loading manifest: %w", err)
	}

	router := intentrouter.NewRouter(0.3)
	engine := execution.NewEngine(nil)
	store := execution.NewMemoryStore()
	engine.SetStore(store) // without this, a suspending step fails loudly

	registry.NewRegistry(router, engine).RegisterDomain(*domain)
	return router, engine, store, nil
}

func main() {
	router, engine, store, err := buildRuntime()
	if err != nil {
		fmt.Fprintln(os.Stderr, "setup failed:", err)
		os.Exit(1)
	}

	match := router.Match("expense-approval", "review this expense claim")
	if !match.Matched {
		fmt.Fprintln(os.Stderr, "no intent matched")
		os.Exit(1)
	}
	fmt.Printf("matched %s (confidence %.2f) -> workflow %s\n\n",
		match.NodeRef, match.Confidence, match.EntryStepRef)

	fmt.Println("== Scenario 1: under the threshold, no human needed ==")
	small, err := engine.Run(match.EntryStepRef, map[string]any{"amount": 240.0, "claimant": "wei"})
	report(small, err)

	fmt.Println()
	fmt.Println("== Scenario 2: over the threshold, parked for a manager ==")
	large, err := engine.Run(match.EntryStepRef, map[string]any{"amount": 4800.0, "claimant": "wei"})
	report(large, err)

	// What an operator would see while the run is parked.
	for _, parked := range store.Pending() {
		fmt.Printf("  pending: %s\n", parked.Reason)
		fmt.Printf("  context preserved across the pause: claimant=%v amount=%v\n",
			parked.Ctx["claimant"], parked.Ctx["amount"])
	}

	fmt.Println()
	fmt.Println("== Scenario 2 continued: the manager approves ==")
	resumed, err := engine.Resume(large.StatePointer, map[string]any{"approved": true})
	report(resumed, err)

	fmt.Println()
	fmt.Println("== The same pointer cannot be used twice ==")
	if _, err := engine.Resume(large.StatePointer, map[string]any{"approved": true}); err != nil {
		fmt.Printf("  second resume refused: %v\n", err)
	}
}

func report(result entities.WorkflowResult, err error) {
	if err != nil {
		fmt.Printf("  halted: %v\n", err)
		return
	}
	if result.StatePointer != "" && result.Output["outcome"] == nil {
		fmt.Printf("  suspended, resume pointer issued (%s...)\n", result.StatePointer[:8])
		fmt.Printf("  trace: %v\n", result.Trace)
		return
	}
	fmt.Printf("  outcome: %v\n", result.Output["outcome"])
	fmt.Printf("  trace:   %v\n", result.Trace)
}
