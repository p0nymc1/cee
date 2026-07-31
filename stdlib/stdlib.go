package stdlib

import (
	"fmt"
	"strings"

	"github.com/p0nymc1/cee/entities"
	"github.com/p0nymc1/cee/execution"
)

type Factory func(params map[string]any) (execution.Action, error)

type Library map[string]Factory

func Default() Library {
	return Library{
		"std.set":              setFactory,
		"std.require":          requireFactory,
		"std.rule_check":       ruleCheckFactory,
		"std.suspend":          suspendFactory,
		"std.require_verified": requireVerifiedFactory,
	}
}

func suspendFactory(params map[string]any) (execution.Action, error) {
	reason, ok := params["reason"].(string)
	if !ok || reason == "" {
		return nil, fmt.Errorf("std.suspend requires a non-empty 'reason' string")
	}

	audience, _ := params["audience"].(string)

	return func(ctx map[string]any) (map[string]any, error) {
		if audience == "" {
			return execution.Suspend(reason)
		}
		return execution.SuspendFor(reason, audience)
	}, nil
}

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
