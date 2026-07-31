package execution

import (
	"sync"
	"testing"

	"github.com/p0nymc1/cee/entities"
)

func countingWorkflow(id string) *Workflow {
	return &Workflow{
		WorkflowID:  id,
		EntryStepID: "only",
		Steps: map[string]Step{
			"only": &LeafStep{
				StepID: "only",
				Run: func(ctx map[string]any) (map[string]any, error) {
					return map[string]any{"ran": id}, nil
				},
			},
		},
	}
}

func TestRegisteringWhileRunningIsSafe(t *testing.T) {
	engine := NewEngine(nil)
	engine.RegisterWorkflow(countingWorkflow("served"))

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 50; n++ {
				if _, err := engine.Run("served", map[string]any{}); err != nil {
					t.Errorf("Run failed: %v", err)
					return
				}
			}
		}()
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		for n := 0; n < 50; n++ {
			engine.RegisterWorkflow(countingWorkflow(hotName(n)))
			engine.RegisterPolicy(CircuitBreakerPolicy{PolicyID: hotName(n), FallbackStepRef: "only"})
		}
	}()

	wg.Wait()
}

func TestConcurrentRunsOfTheSameWorkflowDoNotShareContext(t *testing.T) {
	engine := NewEngine(nil)
	engine.RegisterWorkflow(&Workflow{
		WorkflowID:  "echo",
		EntryStepID: "write",
		Steps: map[string]Step{
			"write": &LeafStep{
				StepID: "write",
				Run: func(ctx map[string]any) (map[string]any, error) {
					return map[string]any{"seen": ctx["caller"]}, nil
				},
			},
		},
	})

	var wg sync.WaitGroup
	results := make([]entities.WorkflowResult, 32)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			result, err := engine.Run("echo", map[string]any{"caller": n})
			if err != nil {
				t.Errorf("Run failed: %v", err)
				return
			}
			results[n] = result
		}(i)
	}
	wg.Wait()

	for i, result := range results {
		if result.Output["seen"] != i {
			t.Errorf("run %d saw caller %v; concurrent runs must not share context", i, result.Output["seen"])
		}
	}
}

func TestSettersAreSafeWhileRunning(t *testing.T) {
	engine := NewEngine(nil)
	engine.RegisterWorkflow(countingWorkflow("served"))

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for n := 0; n < 100; n++ {
			if _, err := engine.Run("served", map[string]any{}); err != nil {
				t.Errorf("Run failed: %v", err)
				return
			}
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		for n := 0; n < 100; n++ {
			engine.SetStore(NewMemoryStore())
			engine.SetLimits(1000+n, 32)
			engine.SetObserver(nil)
		}
	}()

	wg.Wait()
}

func hotName(n int) string {
	return "hot-" + string(rune('a'+n%26)) + string(rune('a'+n/26))
}
