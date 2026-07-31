package llminjector

import (
	"fmt"
	"sort"

	"github.com/p0nymc1/cee/entities"
)

type FieldType int

const (
	FieldString FieldType = iota
	FieldFloat64
	FieldBool
)

type Schema map[string]FieldType

type Extractor func(rawText string) (map[string]any, error)

type registration struct {
	schema    Schema
	extractor Extractor
}

type Observer interface {
	ObserveExtraction(schemaRef string)
}

type ResultObserver interface {
	ObserveExtractionResult(req entities.ExtractionRequest, result entities.ExtractionResult)
}

type Injector struct {
	registrations map[string]registration
	observer      Observer
}

func NewInjector() *Injector {
	return &Injector{registrations: make(map[string]registration)}
}

func (inj *Injector) SetObserver(o Observer) {
	inj.observer = o
}

func (inj *Injector) RegisterSchema(schemaRef string, schema Schema, extractor Extractor) {
	inj.registrations[schemaRef] = registration{schema: schema, extractor: extractor}
}

func (inj *Injector) Extract(req entities.ExtractionRequest) entities.ExtractionResult {
	result := inj.extract(req)

	if observer, ok := inj.observer.(ResultObserver); ok {
		observer.ObserveExtractionResult(req, result)
	}
	return result
}

func (inj *Injector) extract(req entities.ExtractionRequest) entities.ExtractionResult {
	reg, ok := inj.registrations[req.SchemaRef]
	if !ok {
		return entities.ExtractionResult{
			Success:          false,
			ValidationErrors: []string{fmt.Sprintf("no schema registered for %q", req.SchemaRef)},
		}
	}

	if inj.observer != nil {
		inj.observer.ObserveExtraction(req.SchemaRef)
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

	derived := make([]string, 0, len(clean))
	for field := range clean {
		derived = append(derived, field)
	}
	sort.Strings(derived)

	return entities.ExtractionResult{Success: true, StructuredPayload: clean, ModelDerived: derived}
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

func ContextFrom(base map[string]any, result entities.ExtractionResult) map[string]any {
	ctx := make(map[string]any, len(base)+len(result.StructuredPayload)+1)
	for k, v := range base {
		ctx[k] = v
	}

	derived := map[string]bool{}
	for _, field := range existingProvenance(base) {
		derived[field] = true
	}
	for k, v := range result.StructuredPayload {
		ctx[k] = v
	}
	for _, field := range result.ModelDerived {
		derived[field] = true
	}

	if len(derived) == 0 {
		return ctx
	}
	names := make([]string, 0, len(derived))
	for field := range derived {
		names = append(names, field)
	}
	sort.Strings(names)
	ctx[entities.ModelDerivedKey] = names
	return ctx
}

func existingProvenance(ctx map[string]any) []string {
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
