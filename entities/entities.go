// Package entities defines the shared data contracts exchanged between
// CEE's four core engines. Domain plugins and engine internals only ever
// pass these shapes to one another -- never ad-hoc maps -- so a domain's
// business logic or an engine's internal implementation can be swapped
// without touching the other side.
package entities

// IntentNode is a registrable, matchable intent within one domain's
// namespace.
type IntentNode struct {
	NodeID       string
	DomainID     string
	Examples     []string
	EntryStepRef string
	Metadata     map[string]any
}

// MatchResult is what the intent router reports back for a match attempt.
type MatchResult struct {
	Matched      bool
	NodeRef      string
	Confidence   float64
	EntryStepRef string
}

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
	Output       map[string]any
	StatePointer string
	Trace        []string
}
