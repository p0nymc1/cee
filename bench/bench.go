package bench

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/p0nymc1/cee/execution"
	"github.com/p0nymc1/cee/intentrouter"
	"github.com/p0nymc1/cee/registry"
	"github.com/p0nymc1/cee/scorecard"
)

type Event struct {
	WorkflowRef string         `json:"workflow_ref"`
	Context     map[string]any `json:"context"`
}

type Suite struct {
	PluginName string  `json:"plugin"`
	Events     []Event `json:"events"`
}

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

type Result struct {
	PluginName string
	Events     int
	Errors     int
	scorecard.Scorecard
}

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
