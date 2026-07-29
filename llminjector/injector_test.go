package llminjector

import (
	"testing"

	"github.com/cee-project/cee/entities"
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
