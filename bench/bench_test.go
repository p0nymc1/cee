package bench

import (
	"testing"

	"github.com/p0nymc1/cee/manifest"
	"github.com/p0nymc1/cee/registry"
	"github.com/p0nymc1/cee/stdlib"
)

const slaManifest = `{
  "name": "sla-guard",
  "policies": [{"policy_id": "route_to_breach", "fallback_step_ref": "breach"}],
  "workflows": [{
    "workflow_id": "sla-guard.evaluate",
    "entry_step_id": "check",
    "steps": [
      {"step_id": "check", "type": "leaf", "action_ref": "std.require",
       "with": {"field": "response_minutes", "op": "lte", "value": 60},
       "circuit_breaker_policy_ref": "route_to_breach", "on_success": "met"},
      {"step_id": "met", "type": "leaf", "action_ref": "std.set", "with": {"fields": {"sla_met": true}}},
      {"step_id": "breach", "type": "leaf", "action_ref": "std.set", "with": {"fields": {"sla_breached": true}}}
    ]
  }]
}`

func loadSLA(t *testing.T) *registry.Domain {
	t.Helper()
	domain, err := manifest.Load([]byte(slaManifest), nil, stdlib.Default())
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	return domain
}

func TestRunAggregatesAllEvents(t *testing.T) {
	domain := loadSLA(t)
	suite := Suite{
		PluginName: "sla-guard",
		Events: []Event{
			{WorkflowRef: "sla-guard.evaluate", Context: map[string]any{"response_minutes": 30.0}},  // within: check + met = 2 det steps
			{WorkflowRef: "sla-guard.evaluate", Context: map[string]any{"response_minutes": 120.0}}, // breach: check fails -> breaker -> breach = 1 det step + 1 breaker
		},
	}
	result := Run(*domain, suite)

	if result.Events != 2 || result.Errors != 0 {
		t.Fatalf("expected 2 events 0 errors, got %+v", result)
	}
	// within-sla path: check + met = 2 deterministic steps. breach path:
	// check runs (its comparison is a real deterministic op, it just fails
	// and routes on) + breach = 2 more. A step failing its action still ran;
	// only a sandbox-blocked step goes uncounted. -> 4 deterministic, 0 LLM.
	if result.DeterministicSteps != 4 {
		t.Fatalf("expected 4 deterministic steps, got %d", result.DeterministicSteps)
	}
	if result.LLMCalls != 0 {
		t.Fatalf("expected 0 LLM calls, got %d", result.LLMCalls)
	}
	if result.CircuitBreakerTrips != 1 {
		t.Fatalf("expected 1 breaker trip, got %d", result.CircuitBreakerTrips)
	}
	if result.DeterminismRatio() != 1 {
		t.Fatalf("a pure-deterministic plugin should score 100%%, got %v", result.DeterminismRatio())
	}
}

func TestLeaderboardRanksByDeterminism(t *testing.T) {
	high := Result{PluginName: "high"}
	high.DeterministicSteps = 9
	high.LLMCalls = 1 // 90%
	low := Result{PluginName: "low"}
	low.DeterministicSteps = 1
	low.LLMCalls = 1 // 50%

	ranked := Leaderboard([]Result{low, high})
	if ranked[0].PluginName != "high" || ranked[1].PluginName != "low" {
		t.Fatalf("expected high determinism first, got %s then %s", ranked[0].PluginName, ranked[1].PluginName)
	}
}

func TestParseSuiteRejectsEmpty(t *testing.T) {
	if _, err := ParseSuite([]byte(`{"plugin":"x","events":[]}`)); err == nil {
		t.Fatalf("expected an error for a suite with no events")
	}
}
