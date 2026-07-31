package main

import (
	"fmt"
	"os"

	"github.com/p0nymc1/cee/entities"
	"github.com/p0nymc1/cee/execution"
	"github.com/p0nymc1/cee/intentrouter"
	"github.com/p0nymc1/cee/manifest"
	"github.com/p0nymc1/cee/registry"
	"github.com/p0nymc1/cee/stdlib"
)

const manifestPath = "examples/manifests/expense-approval.json"

func buildRuntime() (*intentrouter.Router, *execution.Engine, *execution.MemoryStore, error) {
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("reading %s: %w", manifestPath, err)
	}

	domain, err := manifest.Load(data, nil, stdlib.Default())
	if err != nil {
		return nil, nil, nil, fmt.Errorf("loading manifest: %w", err)
	}

	router := intentrouter.NewRouter(0.3)
	engine := execution.NewEngine(nil)

	store := execution.NewMemoryStore()
	engine.SetStore(store)

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
		match.NodeRef, match.Confidence, match.EntryWorkflowRef)

	fmt.Println("== Scenario 1: under the threshold, no human needed ==")
	small, err := engine.Run(match.EntryWorkflowRef, map[string]any{"amount": 240.0, "claimant": "wei"})
	report(small, err)

	fmt.Println()
	fmt.Println("== Scenario 2: over the threshold, parked for a manager ==")
	large, err := engine.Run(match.EntryWorkflowRef, map[string]any{"amount": 4800.0, "claimant": "wei"})
	report(large, err)

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

	if result.StatePointer != "" {
		fmt.Printf("  suspended, resume pointer issued (%s...)\n", result.StatePointer[:8])
		fmt.Printf("  trace: %v\n", result.Trace)
		return
	}
	fmt.Printf("  outcome: %v\n", result.Output["outcome"])
	fmt.Printf("  trace:   %v\n", result.Trace)
}
