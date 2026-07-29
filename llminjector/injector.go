// Package llminjector implements CEE's edge LLM injector: the only place in
// the system an LLM is allowed to run, and only to extract structured
// fields out of unstructured text -- never to decide what happens next.
// Whatever an extractor returns is filtered down to the fields declared in
// its target schema before being reported as a success, so a decision field
// can't be smuggled back into the deterministic engine.
package llminjector

import (
	"fmt"

	"cee/entities"
)

// FieldType is the set of primitive kinds an extracted field may declare.
type FieldType int

const (
	FieldString FieldType = iota
	FieldFloat64
	FieldBool
)

// Schema is the field-name -> type contract an extractor's output must
// satisfy.
type Schema map[string]FieldType

// Extractor is a domain-registered function that turns raw text into a
// candidate structured payload -- typically backed by a small/cheap LLM
// call.
type Extractor func(rawText string) (map[string]any, error)

type registration struct {
	schema    Schema
	extractor Extractor
}

// Injector holds the schemas and extractors domains have registered.
type Injector struct {
	registrations map[string]registration
}

func NewInjector() *Injector {
	return &Injector{registrations: make(map[string]registration)}
}

func (inj *Injector) RegisterSchema(schemaRef string, schema Schema, extractor Extractor) {
	inj.registrations[schemaRef] = registration{schema: schema, extractor: extractor}
}

// Extract runs the registered extractor for req.SchemaRef and validates its
// output against the schema. Only schema-declared fields ever make it into
// StructuredPayload.
func (inj *Injector) Extract(req entities.ExtractionRequest) entities.ExtractionResult {
	reg, ok := inj.registrations[req.SchemaRef]
	if !ok {
		return entities.ExtractionResult{
			Success:          false,
			ValidationErrors: []string{fmt.Sprintf("no schema registered for %q", req.SchemaRef)},
		}
	}

	payload, err := reg.extractor(req.RawText)
	if err != nil {
		return entities.ExtractionResult{Success: false, ValidationErrors: []string{err.Error()}}
	}

	var errs []string
	clean := make(map[string]any, len(reg.schema))
	for field, wantType := range reg.schema {
		value, present := payload[field]
		if !present {
			errs = append(errs, fmt.Sprintf("missing field %q", field))
			continue
		}
		if !matchesType(value, wantType) {
			errs = append(errs, fmt.Sprintf("field %q has the wrong type", field))
			continue
		}
		clean[field] = value
	}
	if len(errs) > 0 {
		return entities.ExtractionResult{Success: false, ValidationErrors: errs}
	}
	return entities.ExtractionResult{Success: true, StructuredPayload: clean}
}

func matchesType(value any, wantType FieldType) bool {
	switch wantType {
	case FieldString:
		_, ok := value.(string)
		return ok
	case FieldFloat64:
		_, ok := value.(float64)
		return ok
	case FieldBool:
		_, ok := value.(bool)
		return ok
	default:
		return false
	}
}
