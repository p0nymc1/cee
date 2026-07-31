package llminjector

import (
	"testing"

	"github.com/p0nymc1/cee/entities"
)

func TestExtractionStripsUnschemaFields(t *testing.T) {
	inj := NewInjector()
	inj.RegisterSchema("finance.expense_fields", Schema{
		"amount":   FieldFloat64,
		"category": FieldString,
	}, func(rawText string) (map[string]any, error) {
		return map[string]any{"amount": 4200.0, "category": "travel", "is_fraud": true}, nil
	})

	result := inj.Extract(entities.ExtractionRequest{
		RawText:   "taxi to airport $4200",
		SchemaRef: "finance.expense_fields",
		DomainID:  "finance",
	})
	if !result.Success {
		t.Fatalf("expected success, got errors: %v", result.ValidationErrors)
	}
	if _, present := result.StructuredPayload["is_fraud"]; present {
		t.Fatalf("expected is_fraud to be stripped, payload: %+v", result.StructuredPayload)
	}
	if result.StructuredPayload["amount"] != 4200.0 || result.StructuredPayload["category"] != "travel" {
		t.Fatalf("unexpected payload: %+v", result.StructuredPayload)
	}
}

func TestMissingFieldFailsValidation(t *testing.T) {
	inj := NewInjector()
	inj.RegisterSchema("finance.expense_fields", Schema{
		"amount":   FieldFloat64,
		"category": FieldString,
	}, func(rawText string) (map[string]any, error) {
		return map[string]any{"amount": 10.0}, nil
	})

	result := inj.Extract(entities.ExtractionRequest{RawText: "x", SchemaRef: "finance.expense_fields", DomainID: "finance"})
	if result.Success {
		t.Fatalf("expected failure")
	}
}

func TestUnregisteredSchemaFails(t *testing.T) {
	inj := NewInjector()
	result := inj.Extract(entities.ExtractionRequest{RawText: "x", SchemaRef: "unknown", DomainID: "finance"})
	if result.Success {
		t.Fatalf("expected failure for unregistered schema")
	}
}

func TestExtractionLabelsEveryFieldAsModelDerived(t *testing.T) {
	inj := NewInjector()
	inj.RegisterSchema("finance.expense_fields",
		Schema{"amount": FieldFloat64, "category": FieldString},
		func(string) (map[string]any, error) {
			return map[string]any{"amount": 4200.0, "category": "travel", "is_fraud": true}, nil
		})

	result := inj.Extract(entities.ExtractionRequest{RawText: "x", SchemaRef: "finance.expense_fields"})
	if !result.Success {
		t.Fatalf("unexpected failure: %v", result.ValidationErrors)
	}

	if len(result.ModelDerived) != 2 {
		t.Fatalf("expected both fields labelled, got %v", result.ModelDerived)
	}
	if result.ModelDerived[0] != "amount" || result.ModelDerived[1] != "category" {
		t.Fatalf("provenance should be sorted for stable replay, got %v", result.ModelDerived)
	}

	for _, name := range result.ModelDerived {
		if name == "is_fraud" {
			t.Fatal("a stripped field must not appear in provenance")
		}
	}
}

func TestContextFromCarriesProvenanceAlongsideAuthoritativeFields(t *testing.T) {
	inj := NewInjector()
	inj.RegisterSchema("s", Schema{"amount": FieldFloat64},
		func(string) (map[string]any, error) { return map[string]any{"amount": 4200.0}, nil })
	result := inj.Extract(entities.ExtractionRequest{RawText: "x", SchemaRef: "s"})

	ctx := ContextFrom(map[string]any{"account_id": "acct-1"}, result)

	if ctx["account_id"] != "acct-1" || ctx["amount"] != 4200.0 {
		t.Fatalf("both values should be present, got %v", ctx)
	}
	derived, _ := ctx[entities.ModelDerivedKey].([]string)
	if len(derived) != 1 || derived[0] != "amount" {
		t.Fatalf("only the extracted field is model-derived, got %v", derived)
	}
}

func TestContextFromAccumulatesProvenanceAcrossExtractions(t *testing.T) {
	inj := NewInjector()
	inj.RegisterSchema("a", Schema{"amount": FieldFloat64},
		func(string) (map[string]any, error) { return map[string]any{"amount": 1.0}, nil })
	inj.RegisterSchema("b", Schema{"merchant": FieldString},
		func(string) (map[string]any, error) { return map[string]any{"merchant": "acme"}, nil })

	ctx := ContextFrom(nil, inj.Extract(entities.ExtractionRequest{SchemaRef: "a"}))
	ctx = ContextFrom(ctx, inj.Extract(entities.ExtractionRequest{SchemaRef: "b"}))

	derived, _ := ctx[entities.ModelDerivedKey].([]string)
	if len(derived) != 2 || derived[0] != "amount" || derived[1] != "merchant" {
		t.Fatalf("both extractions should be recorded, got %v", derived)
	}
}

func TestContextFromAddsNothingWhenNothingWasExtracted(t *testing.T) {
	ctx := ContextFrom(map[string]any{"account_id": "acct-1"}, entities.ExtractionResult{Success: true})
	if _, present := ctx[entities.ModelDerivedKey]; present {
		t.Fatalf("expected no provenance key, got %v", ctx)
	}
}
