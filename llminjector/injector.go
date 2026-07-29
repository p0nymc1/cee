// Package llminjector implements CEE's edge LLM injector: the only place in
// the system an LLM is allowed to run, and only to extract structured
// fields out of unstructured text -- never to decide what happens next.
// Whatever an extractor returns is filtered down to the fields declared in
// its target schema before being reported as a success, so a decision field
// can't be smuggled back into the deterministic engine.
package llminjector

import (
	"fmt"
	"sort"

	"github.com/p0nymc1/cee/entities"
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

// Observer is notified each time an extraction actually invokes its
// extractor -- i.e. each time the edge LLM is called. A scorecard recorder
// uses this to count the (few) non-deterministic operations in a run
// against the (many) deterministic engine steps. A failed schema lookup is
// not an LLM call and is not reported.
type Observer interface {
	ObserveExtraction(schemaRef string)
}

// ResultObserver is an optional extension of Observer that also receives what
// an extraction produced.
//
// It exists so a run that used a model can be replayed. An extraction is a
// non-deterministic input in exactly the way a sandbox probe is -- ask the
// model again tomorrow and it may answer differently -- so reproducing a run
// means substituting what it answered at the time, not asking again. Counting
// calls, which Observer alone allows, is not enough to do that.
//
// Kept separate from Observer rather than folded into it so existing
// implementations (scorecard.Recorder) keep working untouched; the injector
// type-asserts for it and skips the callback when it is absent.
type ResultObserver interface {
	ObserveExtractionResult(req entities.ExtractionRequest, result entities.ExtractionResult)
}

// Injector holds the schemas and extractors domains have registered.
type Injector struct {
	registrations map[string]registration
	observer      Observer
}

func NewInjector() *Injector {
	return &Injector{registrations: make(map[string]registration)}
}

// SetObserver attaches an Observer for metrics collection. Passing nil (the
// default) disables observation with zero overhead.
func (inj *Injector) SetObserver(o Observer) {
	inj.observer = o
}

func (inj *Injector) RegisterSchema(schemaRef string, schema Schema, extractor Extractor) {
	inj.registrations[schemaRef] = registration{schema: schema, extractor: extractor}
}

// Extract runs the registered extractor for req.SchemaRef and validates its
// output against the schema. Only schema-declared fields ever make it into
// StructuredPayload.
func (inj *Injector) Extract(req entities.ExtractionRequest) entities.ExtractionResult {
	result := inj.extract(req)
	// Reported on every path, success or failure alike: a run that failed to
	// extract has to replay as a run that failed to extract.
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

	// The extractor is about to be invoked -- this is the run's one
	// non-deterministic (edge LLM) operation, so report it for scoring.
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

	// Every field here came out of a model. That is recorded structurally,
	// exactly like the stripping above: the injector is the only place
	// extraction happens, so it is the only place that can know this for
	// certain, and an extractor cannot opt out of being labelled.
	derived := make([]string, 0, len(clean))
	for field := range clean {
		derived = append(derived, field)
	}
	sort.Strings(derived) // stable, so a recorded run replays identically

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

// ContextFrom merges an extraction into a workflow context, carrying the
// provenance with it.
//
// It exists because the alternative -- callers merging StructuredPayload
// themselves -- silently discards which fields were guessed, and a value's
// origin becomes unknowable the moment it lands in a plain map. Making the
// correct merge the easy one is the only thing that keeps std.require_verified
// meaningful downstream.
//
// base may be nil. Fields already in base are overwritten by the extraction,
// and any provenance base was already carrying is preserved alongside the new
// entries.
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

// existingProvenance reads a provenance list already in a context, tolerating
// the []any shape a list takes after a JSON round trip through a Store.
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
