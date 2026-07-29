// Package bench runs a batch of standard events through a domain plugin and
// scores the result against a naive-agent baseline, turning "more efficient
// than an agent" from a claim into a ranked, reproducible number.
//
// The baseline is the same honest model the scorecard uses: a naive agent
// makes one LLM call per cognitive operation. So across a whole suite, the
// aggregate determinism ratio is exactly the fraction of LLM calls the
// plugin eliminated versus that agent -- no token estimation required. A
// leaderboard sorts plugins by that fraction, which is the social,
// competitive signal that pulls contributors to optimize and publish.
package bench

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/cee-project/cee/execution"
	"github.com/cee-project/cee/intentrouter"
	"github.com/cee-project/cee/registry"
	"github.com/cee-project/cee/scorecard"
)

// Event is one benchmark input: a workflow to run and the context to run it
// with.
type Event struct {
	WorkflowRef string         `json:"workflow_ref"`
	Context     map[string]any `json:"context"`
}

// Suite is a plugin's benchmark fixture.
type Suite struct {
	PluginName string  `json:"plugin"`
	Events     []Event `json:"events"`
}

// ParseSuite decodes a benchmark fixture.
func ParseSuite(data []byte) (Suite, error) {
	var s Suite
	if err := json.Unmarshal(data, &s); err != nil {
		return Suite{}, fmt.Errorf("bench: invalid suite JSON: %w", err)
	}
	if len(s.Events) == 0 {
		return Suite{}, fmt.Errorf("bench: suite %q has no events", s.PluginName)
	}
	return s, nil
}

// Result is a suite's aggregate outcome. It embeds a scorecard.Scorecard so
// DeterminismRatio and LLMCallsEliminatedVsAgent are available directly.
type Result struct {
	PluginName string
	Events     int
	Errors     int
	scorecard.Scorecard
}

// Run registers domain into a fresh engine, attaches a scorecard recorder,
// and runs every event in suite, accumulating one aggregate scorecard. A
// per-event error (e.g. a circuit breaker with no fallback) is counted and
// the batch continues, so one bad event never hides the rest of the numbers.
//
// The engine is built without a sandbox: L1 catalog plugins do not use
// probes (probes require Go). A suite whose manifest references a probe will
// surface as per-event errors rather than a panic.
func Run(domain registry.Domain, suite Suite) Result {
	router := intentrouter.NewRouter(0.3)
	engine := execution.NewEngine(nil)
	registry.NewRegistry(router, engine).RegisterDomain(domain)

	recorder := scorecard.NewRecorder()
	engine.SetObserver(recorder)

	result := Result{PluginName: suite.PluginName, Events: len(suite.Events)}
	for _, ev := range suite.Events {
		if _, err := engine.Run(ev.WorkflowRef, ev.Context); err != nil {
			result.Errors++
		}
	}
	result.Scorecard = recorder.Snapshot(suite.PluginName)
	return result
}

// Leaderboard returns results ranked by determinism ratio (desc), breaking
// ties by more cognitive operations measured, then by name for stability.
func Leaderboard(results []Result) []Result {
	ranked := make([]Result, len(results))
	copy(ranked, results)
	sort.SliceStable(ranked, func(i, j int) bool {
		di, dj := ranked[i].DeterminismRatio(), ranked[j].DeterminismRatio()
		if di != dj {
			return di > dj
		}
		if ranked[i].CognitiveOps() != ranked[j].CognitiveOps() {
			return ranked[i].CognitiveOps() > ranked[j].CognitiveOps()
		}
		return ranked[i].PluginName < ranked[j].PluginName
	})
	return ranked
}

// FormatLeaderboard renders a ranked leaderboard as aligned text.
func FormatLeaderboard(results []Result) string {
	ranked := Leaderboard(results)
	out := fmt.Sprintf("%-4s %-16s %-12s %-10s %-8s %s\n", "rank", "plugin", "determinism", "events", "errors", "LLM calls eliminated vs agent")
	for i, r := range ranked {
		out += fmt.Sprintf("%-4d %-16s %-12s %-10d %-8d %d of %d\n",
			i+1, r.PluginName, fmt.Sprintf("%.0f%%", r.DeterminismRatio()*100),
			r.Events, r.Errors, r.LLMCallsEliminatedVsAgent(), r.CognitiveOps())
	}
	return out
}
