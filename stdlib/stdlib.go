// Package stdlib is CEE's standard action library: a set of generic,
// business-agnostic, deterministic actions that a manifest can reference by
// name and configure with pure JSON -- no Go required from the plugin
// author. This is what makes the L1 "no-code" contribution tier real: a
// contributor who cannot write Go can still publish a runnable domain
// plugin by composing these actions in a manifest.
//
// Every standard action is a Factory: it takes the manifest step's "with"
// parameters, validates them once at load time, and returns a bound
// execution.Action. Factories never call an LLM and never carry hidden
// state -- they are the deterministic vocabulary the engine runs.
package stdlib

import (
	"fmt"
	"strings"

	"github.com/p0nymc1/cee/entities"
	"github.com/p0nymc1/cee/execution"
)

// Factory builds a bound action from a step's declared parameters. It
// validates params eagerly and returns an error if they are malformed, so a
// misconfigured manifest fails at load time, not mid-run.
type Factory func(params map[string]any) (execution.Action, error)

// Library maps a standard action name to its factory.
type Library map[string]Factory

// Default returns the built-in standard action library. The names are
// conventionally prefixed "std." so they never collide with a domain's own
// custom hook names.
func Default() Library {
	return Library{
		"std.set":              setFactory,
		"std.require":          requireFactory,
		"std.rule_check":       ruleCheckFactory,
		"std.suspend":          suspendFactory,
		"std.require_verified": requireVerifiedFactory,
	}
}

// suspendFactory parks the run pending something outside the engine -- a
// human decision, a callback, a scheduled window. The engine saves the
// context and returns a resume pointer; whatever the external event decides
// is merged into context on resume, so the step after this one can branch on
// it with std.require like any other field. This is what lets a no-code
// manifest express "hold this for a human" without writing Go.
//
//	{"action_ref": "std.suspend", "with": {"reason": "awaiting human approval"},
//	 "on_success": "apply_decision"}
func suspendFactory(params map[string]any) (execution.Action, error) {
	reason, ok := params["reason"].(string)
	if !ok || reason == "" {
		return nil, fmt.Errorf("std.suspend requires a non-empty 'reason' string")
	}
	return func(ctx map[string]any) (map[string]any, error) {
		return execution.Suspend(reason)
	}, nil
}

// setFactory writes a fixed set of fields into the step's output. Use it for
// terminal/marker steps such as {"flagged": true} or {"contained": true}.
//
//	{"action_ref": "std.set", "with": {"fields": {"flagged": true}}}
func setFactory(params map[string]any) (execution.Action, error) {
	raw, ok := params["fields"]
	if !ok {
		return nil, fmt.Errorf("std.set requires a 'fields' object")
	}
	fields, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("std.set 'fields' must be an object")
	}
	frozen := make(map[string]any, len(fields))
	for k, v := range fields {
		frozen[k] = v
	}
	return func(ctx map[string]any) (map[string]any, error) {
		out := make(map[string]any, len(frozen))
		for k, v := range frozen {
			out[k] = v
		}
		return out, nil
	}, nil
}

// requireFactory is the deterministic gate. It compares a context field
// against a value; if the requirement holds the step passes (and the engine
// continues to on_success), otherwise the step FAILS -- which routes through
// the step's circuit_breaker_policy_ref to a fallback step. This is how a
// no-code manifest expresses branching without the engine needing an
// if/else primitive: "require amount <= threshold; on failure the breaker
// sends us to the alert step".
//
//	{"action_ref": "std.require", "with": {"field": "amount", "op": "lte", "value": 10000},
//	 "circuit_breaker_policy_ref": "route_to_alert"}
func requireFactory(params map[string]any) (execution.Action, error) {
	field, op, want, err := comparisonParams("std.require", params)
	if err != nil {
		return nil, err
	}
	return func(ctx map[string]any) (map[string]any, error) {
		ok, err := compare(ctx[field], op, want)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("requirement not met: field %q (%v) %s %v", field, ctx[field], op, want)
		}
		return map[string]any{}, nil
	}, nil
}

// ruleCheckFactory computes a boolean classification into result_field
// without ever failing. Unlike std.require it does not affect control flow;
// it annotates the result. This is a deterministic rule-engine primitive,
// not an LLM decision -- it is allowed to produce a judgement field because
// the judgement is code, fully reproducible and auditable.
//
//	{"action_ref": "std.rule_check", "with": {"field": "amount", "op": "gt", "value": 10000, "result_field": "is_high_value"}}
func ruleCheckFactory(params map[string]any) (execution.Action, error) {
	field, op, want, err := comparisonParams("std.rule_check", params)
	if err != nil {
		return nil, err
	}
	resultField, ok := params["result_field"].(string)
	if !ok || resultField == "" {
		return nil, fmt.Errorf("std.rule_check requires a non-empty 'result_field' string")
	}
	return func(ctx map[string]any) (map[string]any, error) {
		ok, err := compare(ctx[field], op, want)
		if err != nil {
			return nil, err
		}
		return map[string]any{resultField: ok}, nil
	}, nil
}

// comparisonParams pulls the shared {field, op, value} triple used by
// require and rule_check.
func comparisonParams(action string, params map[string]any) (field, op string, want any, err error) {
	field, ok := params["field"].(string)
	if !ok || field == "" {
		return "", "", nil, fmt.Errorf("%s requires a non-empty 'field' string", action)
	}
	op, ok = params["op"].(string)
	if !ok || !validOps[op] {
		return "", "", nil, fmt.Errorf("%s requires 'op' to be one of eq/neq/gt/gte/lt/lte/in", action)
	}
	want, ok = params["value"]
	if !ok {
		return "", "", nil, fmt.Errorf("%s requires a 'value'", action)
	}
	return field, op, want, nil
}

var validOps = map[string]bool{
	"eq": true, "neq": true, "gt": true, "gte": true, "lte": true, "lt": true, "in": true,
}

func compare(got any, op string, want any) (bool, error) {
	switch op {
	case "eq":
		return equals(got, want), nil
	case "neq":
		return !equals(got, want), nil
	case "in":
		list, ok := want.([]any)
		if !ok {
			return false, fmt.Errorf("op 'in' requires 'value' to be an array")
		}
		for _, item := range list {
			if equals(got, item) {
				return true, nil
			}
		}
		return false, nil
	case "gt", "gte", "lt", "lte":
		g, err1 := toFloat(got)
		w, err2 := toFloat(want)
		if err1 != nil || err2 != nil {
			return false, fmt.Errorf("op %q requires numeric operands, got %v and %v", op, got, want)
		}
		switch op {
		case "gt":
			return g > w, nil
		case "gte":
			return g >= w, nil
		case "lt":
			return g < w, nil
		default:
			return g <= w, nil
		}
	default:
		return false, fmt.Errorf("unknown op %q", op)
	}
}

func equals(a, b any) bool {
	if af, err1 := toFloat(a); err1 == nil {
		if bf, err2 := toFloat(b); err2 == nil {
			return af == bf
		}
	}
	return a == b
}

func toFloat(v any) (float64, error) {
	switch n := v.(type) {
	case float64:
		return n, nil
	case float32:
		return float64(n), nil
	case int:
		return float64(n), nil
	case int64:
		return float64(n), nil
	default:
		return 0, fmt.Errorf("not numeric: %v", v)
	}
}

// requireVerifiedFactory refuses to let a step run on values a model guessed.
//
// Stripping decision fields stops an extractor from saying what should happen.
// It does nothing about a misread fact: an extractor that reads $50,000 as
// $5,000 has decided nothing and has still decided everything, because the
// rules downstream will confidently approve. This is the gate a consequential
// step puts in front of itself -- pay out, isolate a host, disable an account
// -- to say that those particular values must be known rather than guessed.
//
// Failing routes through the step's circuit breaker like any other failure, so
// the usual answer is a fallback that puts it in front of a human.
//
//	{"action_ref": "std.require_verified", "with": {"fields": ["amount", "account"]},
//	 "circuit_breaker_policy_ref": "needs_human_check"}
//
// There is deliberately no companion action that marks a field verified.
// Anything that could stamp "verified" from inside a manifest would be a
// laundering tool: extract a number, mark it checked, act on it. Promoting a
// value is a Go hook's job, and only after it has been corroborated against a
// system of record or a person -- see the normative handbook.
func requireVerifiedFactory(params map[string]any) (execution.Action, error) {
	raw, ok := params["fields"]
	if !ok {
		return nil, fmt.Errorf("std.require_verified requires a 'fields' array")
	}
	list, ok := raw.([]any)
	if !ok || len(list) == 0 {
		return nil, fmt.Errorf("std.require_verified 'fields' must be a non-empty array")
	}
	fields := make([]string, 0, len(list))
	for _, item := range list {
		name, ok := item.(string)
		if !ok || name == "" {
			return nil, fmt.Errorf("std.require_verified 'fields' must contain non-empty strings")
		}
		fields = append(fields, name)
	}

	return func(ctx map[string]any) (map[string]any, error) {
		derived := map[string]bool{}
		for _, name := range provenanceOf(ctx) {
			derived[name] = true
		}
		var guessed []string
		for _, field := range fields {
			if derived[field] {
				guessed = append(guessed, field)
			}
		}
		if len(guessed) > 0 {
			return nil, fmt.Errorf(
				"refusing to act on model-derived %s: extracted values are not verified facts",
				strings.Join(guessed, ", "))
		}
		return map[string]any{}, nil
	}, nil
}

// provenanceOf reads the model-derived list, tolerating the []any shape it
// takes after a JSON round trip through a suspended run's Store.
func provenanceOf(ctx map[string]any) []string {
	switch v := ctx[entities.ModelDerivedKey].(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}
