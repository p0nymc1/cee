package entities

type IntentNode struct {
	NodeID           string
	DomainID         string
	Examples         []string
	EntryWorkflowRef string
	Metadata         map[string]any
}

type MatchResult struct {
	Matched          bool
	NodeRef          string
	Confidence       float64
	EntryWorkflowRef string
}

const ModelDerivedKey = "cee.model_derived"

type ExtractionRequest struct {
	RawText   string
	SchemaRef string
	DomainID  string
}

type ExtractionResult struct {
	Success           bool
	StructuredPayload map[string]any
	ValidationErrors  []string

	ModelDerived []string
}

type ProbeRequest struct {
	ProbeRef    string
	DomainID    string
	StepContext map[string]any
}

type ProbeResult struct {
	Healthy             bool
	DetectedFailureMode string
}

type WorkflowResult struct {
	Output map[string]any

	StatePointer string
	Trace        []string
}
