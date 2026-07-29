// Package entities defines the shared data contracts exchanged between
// CEE's four core engines. Domain plugins and engine internals only ever
// pass these shapes to one another -- never ad-hoc maps -- so a domain's
// business logic or an engine's internal implementation can be swapped
// without touching the other side.
package entities

// IntentNode is a registrable, matchable intent within one domain's
// namespace.
type IntentNode struct {
	NodeID           string
	DomainID         string
	Examples         []string
	EntryWorkflowRef string
	Metadata         map[string]any
}

// MatchResult is what the intent router reports back for a match attempt.
type MatchResult struct {
	Matched          bool
	NodeRef          string
	Confidence       float64
	EntryWorkflowRef string
}

// ModelDerivedKey is the workflow-context key under which the list of
// model-produced field names travels, so provenance survives the hop from
// extraction into execution. Namespaced like the engine's own keys, because a
// domain field must never collide with it.
const ModelDerivedKey = "cee.model_derived"

// ExtractionRequest asks the edge LLM injector to pull structured fields out
// of unstructured text, constrained to a named schema.
type ExtractionRequest struct {
	RawText   string
	SchemaRef string
	DomainID  string
}

// ExtractionResult is the edge LLM injector's response. StructuredPayload is
// only ever populated with fields declared in the target schema -- the
// injector strips anything else before returning, so an extractor cannot
// smuggle a decision field back into the deterministic engine.
type ExtractionResult struct {
	Success           bool
	StructuredPayload map[string]any
	ValidationErrors  []string

	// ModelDerived names the fields in StructuredPayload that a model
	// produced. Stripping decision fields stops an extractor from saying what
	// should happen, but it does nothing about a misread fact: an extractor
	// that reads $50,000 as $5,000 has not decided anything and has still
	// decided everything, because the deterministic rules downstream will
	// confidently approve. Carrying provenance is what lets a consequential
	// step refuse to act on a value that was guessed rather than known.
	//
	// This is deliberately a list of names rather than a confidence score.
	// A model's self-reported confidence is not something the engine can
	// audit, and a number nobody can check is worse than no number at all --
	// it manufactures assurance. Whether a value came from a model is instead
	// a structural fact, known for certain at the point it is produced.
	ModelDerived []string
}

// ProbeRequest asks the sandbox to simulate a step's side effect before the
// deterministic engine commits to running it for real.
type ProbeRequest struct {
	ProbeRef    string
	DomainID    string
	StepContext map[string]any
}

// ProbeResult reports whether the simulated side effect looked safe.
type ProbeResult struct {
	Healthy             bool
	DetectedFailureMode string
}

// WorkflowResult is what the deterministic execution engine returns once a
// workflow's step DAG has run to completion.
type WorkflowResult struct {
	Output map[string]any
	// StatePointer is the token that resumes a suspended run, and is empty
	// for a run that finished. It is therefore the test for "did this park?"
	// -- a non-empty pointer means the workflow is waiting on something
	// outside the engine and Resume will pick it up.
	StatePointer string
	Trace        []string
}
