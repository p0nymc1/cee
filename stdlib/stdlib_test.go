package stdlib

import (
	"errors"
	"strings"
	"testing"

	"github.com/p0nymc1/cee/entities"
	"github.com/p0nymc1/cee/execution"
)

func TestSetWritesFields(t *testing.T) {
	action, err := Default()["std.set"](map[string]any{"fields": map[string]any{"flagged": true}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out, err := action(map[string]any{})
	if err != nil || out["flagged"] != true {
		t.Fatalf("expected flagged=true, got %+v err=%v", out, err)
	}
}

func TestSetRequiresFields(t *testing.T) {
	if _, err := Default()["std.set"](map[string]any{}); err == nil {
		t.Fatalf("expected error when 'fields' missing")
	}
}

func TestRequirePassesWhenSatisfied(t *testing.T) {
	action, err := Default()["std.require"](map[string]any{"field": "amount", "op": "lte", "value": 10000.0})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := action(map[string]any{"amount": 500.0}); err != nil {
		t.Fatalf("expected requirement to pass, got %v", err)
	}
}

func TestRequireFailsWhenViolated(t *testing.T) {
	action, _ := Default()["std.require"](map[string]any{"field": "amount", "op": "lte", "value": 10000.0})
	if _, err := action(map[string]any{"amount": 99999.0}); err == nil {
		t.Fatalf("expected requirement to fail so the circuit breaker can route elsewhere")
	}
}

func TestRuleCheckComputesBoolean(t *testing.T) {
	action, err := Default()["std.rule_check"](map[string]any{
		"field": "amount", "op": "gt", "value": 10000.0, "result_field": "is_high_value",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out, _ := action(map[string]any{"amount": 20000.0})
	if out["is_high_value"] != true {
		t.Fatalf("expected is_high_value=true, got %+v", out)
	}
}

func TestComparisonRejectsBadParams(t *testing.T) {
	cases := []map[string]any{
		{"op": "lte", "value": 1.0},                 // missing field
		{"field": "x", "value": 1.0},                // missing op
		{"field": "x", "op": "bogus", "value": 1.0}, // invalid op
		{"field": "x", "op": "lte"},                 // missing value
	}
	for i, params := range cases {
		if _, err := Default()["std.require"](params); err == nil {
			t.Fatalf("case %d: expected error for params %+v", i, params)
		}
	}
}

func TestInOperator(t *testing.T) {
	action, err := Default()["std.require"](map[string]any{
		"field": "status", "op": "in", "value": []any{"open", "pending"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := action(map[string]any{"status": "open"}); err != nil {
		t.Fatalf("expected 'open' to be in the set, got %v", err)
	}
	if _, err := action(map[string]any{"status": "closed"}); err == nil {
		t.Fatalf("expected 'closed' to be rejected")
	}
}

func TestSuspendRequiresAReason(t *testing.T) {
	if _, err := Default()["std.suspend"](map[string]any{}); err == nil {
		t.Fatal("expected an error when 'reason' is missing")
	}
	if _, err := Default()["std.suspend"](map[string]any{"reason": ""}); err == nil {
		t.Fatal("expected an error when 'reason' is empty")
	}
}

func TestSuspendReturnsASuspension(t *testing.T) {
	action, err := Default()["std.suspend"](map[string]any{"reason": "awaiting human approval"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, err = action(map[string]any{})

	var suspended *execution.Suspended
	if !errors.As(err, &suspended) {
		t.Fatalf("expected *execution.Suspended, got %v", err)
	}
	if suspended.Reason != "awaiting human approval" {
		t.Fatalf("the reason must reach the operator, got %q", suspended.Reason)
	}
}

func TestRequireVerifiedRejectsMalformedParams(t *testing.T) {
	for name, params := range map[string]map[string]any{
		"missing fields": {},
		"not an array":   {"fields": "amount"},
		"empty array":    {"fields": []any{}},
		"non-string":     {"fields": []any{42}},
	} {
		if _, err := Default()["std.require_verified"](params); err == nil {
			t.Fatalf("%s should have been rejected at load time", name)
		}
	}
}

// The gate a consequential step puts in front of itself.
func TestRequireVerifiedRefusesModelDerivedValues(t *testing.T) {
	action, err := Default()["std.require_verified"](map[string]any{"fields": []any{"amount"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, err = action(map[string]any{
		"amount":                 50000.0,
		entities.ModelDerivedKey: []string{"amount"},
	})
	if err == nil {
		t.Fatal("a model-derived amount must not pass a verified gate")
	}
	if !strings.Contains(err.Error(), "amount") {
		t.Fatalf("the refusal should name the field, got %v", err)
	}
}

func TestRequireVerifiedPassesAuthoritativeValues(t *testing.T) {
	action, _ := Default()["std.require_verified"](map[string]any{"fields": []any{"amount"}})

	// amount came from our own ledger; only merchant was extracted.
	if _, err := action(map[string]any{
		"amount":                 50000.0,
		"merchant":               "acme",
		entities.ModelDerivedKey: []string{"merchant"},
	}); err != nil {
		t.Fatalf("a value nobody guessed should pass: %v", err)
	}
}

// A context that never went near an extractor has no provenance at all, and
// must not be treated as suspect.
func TestRequireVerifiedPassesWhenNothingWasExtracted(t *testing.T) {
	action, _ := Default()["std.require_verified"](map[string]any{"fields": []any{"amount"}})
	if _, err := action(map[string]any{"amount": 50000.0}); err != nil {
		t.Fatalf("expected a plain context to pass, got %v", err)
	}
}

// Provenance survives a suspended run being written to disk and read back,
// where a []string becomes a []any.
func TestRequireVerifiedSurvivesAJSONRoundTrip(t *testing.T) {
	action, _ := Default()["std.require_verified"](map[string]any{"fields": []any{"amount"}})
	if _, err := action(map[string]any{
		"amount":                 50000.0,
		entities.ModelDerivedKey: []any{"amount"},
	}); err == nil {
		t.Fatal("provenance must still be honoured after a JSON round trip")
	}
}
