package main

import (
	"fmt"
	"os"

	"github.com/p0nymc1/cee/execution"
	"github.com/p0nymc1/cee/intentrouter"
	"github.com/p0nymc1/cee/manifest"
	"github.com/p0nymc1/cee/registry"
	"github.com/p0nymc1/cee/sandbox"
	"github.com/p0nymc1/cee/stdlib"
)

var targetVersions = map[string]float64{
	"row-1": 7,
	"row-2": 7,
	"row-3": 7,
}

type runtime struct {
	router *intentrouter.Router
	engine *execution.Engine
	store  *execution.MemoryStore
}

func buildRuntime() (*runtime, error) {
	router := intentrouter.NewRouter(0.3)
	sb := sandbox.NewSandbox()
	engine := execution.NewEngine(sb)
	store := execution.NewMemoryStore()
	engine.SetStore(store)
	reg := registry.NewRegistry(router, engine)

	sb.RegisterProbe("sync.check_target_unchanged", func(ctx map[string]any) (bool, string, error) {
		id, _ := ctx["record_id"].(string)
		seen, _ := ctx["target_version_seen"].(float64)
		current, known := targetVersions[id]
		if !known {
			return false, fmt.Sprintf("target has no row %q", id), nil
		}
		if current != seen {
			return false, fmt.Sprintf("target row %q moved from %v to %v", id, seen, current), nil
		}
		return true, "", nil
	})

	hooks := manifest.Hooks{

		"sync.write_to_target": func(ctx map[string]any) (map[string]any, error) {
			id, _ := ctx["record_id"].(string)
			targetVersions[id]++
			return map[string]any{"written_version": targetVersions[id]}, nil
		},
	}

	for _, spec := range []struct {
		path  string
		hooks manifest.Hooks
	}{
		{"examples/manifests/ticket-routing.json", nil},
		{"examples/manifests/change-window.json", nil},
		{"examples/manifests/record-sync.json", hooks},
	} {
		data, err := os.ReadFile(spec.path)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", spec.path, err)
		}
		domain, err := manifest.Load(data, spec.hooks, stdlib.Default())
		if err != nil {
			return nil, fmt.Errorf("loading %s: %w", spec.path, err)
		}
		reg.RegisterDomain(*domain)
	}

	return &runtime{router: router, engine: engine, store: store}, nil
}

func main() {
	rt, err := buildRuntime()
	if err != nil {
		fmt.Fprintln(os.Stderr, "setup failed:", err)
		os.Exit(1)
	}

	ticketRouting(rt)
	fmt.Println()
	changeWindow(rt)
	fmt.Println()
	recordSync(rt)
}

func ticketRouting(rt *runtime) {
	fmt.Println("== 工单路由 / ticket routing: an N-way switch from binary edges ==")

	for _, ticket := range []map[string]any{
		{"id": "T-1", "severity": "urgent", "category": "bug"},
		{"id": "T-2", "severity": "normal", "category": "billing"},
		{"id": "T-3", "severity": "normal", "category": "crash"},
		{"id": "T-4", "severity": "normal", "category": "how-do-i"},
	} {
		result, err := rt.engine.Run("ticket-routing.triage", ticket)
		if err != nil {
			fmt.Printf("  %v halted: %v\n", ticket["id"], err)
			continue
		}
		fmt.Printf("  %v (%v/%v) -> %-16v trace %v\n",
			ticket["id"], ticket["severity"], ticket["category"],
			result.Output["queue"], result.Trace)
	}
}

func changeWindow(rt *runtime) {
	fmt.Println("== 调度 / scheduling: deferral is suspension, not a timer ==")

	inWindow, err := rt.engine.Run("change-window.apply",
		map[string]any{"change": "bump-tls-ciphers", "in_maintenance_window": true})
	if err != nil {
		fmt.Println("  halted:", err)
		return
	}
	fmt.Printf("  inside the window:  applied=%v trace %v\n", inWindow.Output["applied"], inWindow.Trace)

	deferred, err := rt.engine.Run("change-window.apply",
		map[string]any{"change": "bump-tls-ciphers", "in_maintenance_window": false})
	if err != nil {
		fmt.Println("  halted:", err)
		return
	}
	fmt.Printf("  outside the window: parked, applied=%v trace %v\n", deferred.Output["applied"], deferred.Trace)
	for _, parked := range rt.store.Pending() {
		fmt.Printf("    a scheduler holds: %s\n", parked.Reason)
	}

	resumed, err := rt.engine.Resume(deferred.StatePointer, map[string]any{"in_maintenance_window": true})
	if err != nil {
		fmt.Println("  resume failed:", err)
		return
	}
	fmt.Printf("  window opens:       applied=%v trace %v\n", resumed.Output["applied"], resumed.Trace)
}

func recordSync(rt *runtime) {
	fmt.Println("== 数据同步 / data sync: the loop lives in the caller, not the DAG ==")

	batch := []map[string]any{
		{"record_id": "row-1", "target_version_seen": 7.0},
		{"record_id": "row-2", "target_version_seen": 3.0},
		{"record_id": "", "target_version_seen": 7.0},
		{"record_id": "row-9", "target_version_seen": 7.0},
	}

	synced := 0
	for _, record := range batch {
		result, err := rt.engine.Run("record-sync.push", record)
		if err != nil {
			fmt.Printf("  %-6v halted: %v\n", record["record_id"], err)
			continue
		}
		if result.Output["synced"] == true {
			synced++
			fmt.Printf("  %-6v synced, target now at v%v\n",
				record["record_id"], result.Output["written_version"])
			continue
		}

		fmt.Printf("  %-6v %v\n            because: %v\n",
			record["record_id"], result.Output["outcome"],
			result.Output[execution.FailureReasonKey])
	}
	fmt.Printf("  %d of %d records synced; the rest were held, not retried\n", synced, len(batch))
}
