package replay

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/p0nymc1/cee/entities"
)

type ProbeVerdict struct {
	ProbeRef            string `json:"probe_ref"`
	DomainID            string `json:"domain_id,omitempty"`
	Healthy             bool   `json:"healthy"`
	DetectedFailureMode string `json:"detected_failure_mode,omitempty"`

	Err string `json:"error,omitempty"`
}

type ExtractionRecord struct {
	SchemaRef        string         `json:"schema_ref"`
	DomainID         string         `json:"domain_id,omitempty"`
	RawText          string         `json:"raw_text,omitempty"`
	Success          bool           `json:"success"`
	Payload          map[string]any `json:"payload,omitempty"`
	ModelDerived     []string       `json:"model_derived,omitempty"`
	ValidationErrors []string       `json:"validation_errors,omitempty"`
}

type Recording struct {
	WorkflowID string         `json:"workflow_id"`
	Input      map[string]any `json:"input"`
	Probes     []ProbeVerdict `json:"probes,omitempty"`

	Extractions []ExtractionRecord `json:"extractions,omitempty"`
	Trace       []string           `json:"trace"`
	Output      map[string]any     `json:"output"`

	Suspended  bool      `json:"suspended"`
	Failed     bool      `json:"failed"`
	Error      string    `json:"error,omitempty"`
	RecordedAt time.Time `json:"recorded_at"`
}

type Recorder struct {
	mu          sync.Mutex
	inner       Prober
	verdicts    []ProbeVerdict
	extractions []ExtractionRecord
}

type Prober interface {
	Probe(entities.ProbeRequest) (entities.ProbeResult, error)
}

func NewRecorder(inner Prober) *Recorder {
	return &Recorder{inner: inner}
}

func (r *Recorder) Probe(req entities.ProbeRequest) (entities.ProbeResult, error) {
	if r.inner == nil {
		return entities.ProbeResult{}, fmt.Errorf("replay: no sandbox configured, but %q was probed", req.ProbeRef)
	}
	result, err := r.inner.Probe(req)

	verdict := ProbeVerdict{
		ProbeRef:            req.ProbeRef,
		DomainID:            req.DomainID,
		Healthy:             result.Healthy,
		DetectedFailureMode: result.DetectedFailureMode,
	}
	if err != nil {
		verdict.Err = err.Error()
	}

	r.mu.Lock()
	r.verdicts = append(r.verdicts, verdict)
	r.mu.Unlock()

	return result, err
}

func (r *Recorder) Finish(workflowID string, input map[string]any, result entities.WorkflowResult, runErr error) Recording {
	r.mu.Lock()
	defer r.mu.Unlock()

	rec := Recording{
		WorkflowID:  workflowID,
		Input:       cloneMap(input),
		Probes:      append([]ProbeVerdict(nil), r.verdicts...),
		Extractions: append([]ExtractionRecord(nil), r.extractions...),
		Trace:       append([]string(nil), result.Trace...),
		Output:      cloneMap(result.Output),
		Suspended:   result.StatePointer != "",
		RecordedAt:  time.Now().UTC(),
	}
	if runErr != nil {
		rec.Failed = true
		rec.Error = runErr.Error()
	}
	return rec
}

type Player struct {
	mu          sync.Mutex
	byRef       map[string][]ProbeVerdict
	extractions map[string][]ExtractionRecord
	unmatched   []string

	Fallback Prober
}

func NewPlayer(rec Recording) *Player {
	byRef := make(map[string][]ProbeVerdict, len(rec.Probes))
	for _, v := range rec.Probes {
		byRef[v.ProbeRef] = append(byRef[v.ProbeRef], v)
	}
	extractions := make(map[string][]ExtractionRecord, len(rec.Extractions))
	for _, e := range rec.Extractions {
		extractions[e.SchemaRef] = append(extractions[e.SchemaRef], e)
	}
	return &Player{byRef: byRef, extractions: extractions}
}

func (p *Player) Probe(req entities.ProbeRequest) (entities.ProbeResult, error) {
	p.mu.Lock()
	queued := p.byRef[req.ProbeRef]
	if len(queued) > 0 {
		v := queued[0]

		if len(queued) > 1 {
			p.byRef[req.ProbeRef] = queued[1:]
		}
		p.mu.Unlock()

		result := entities.ProbeResult{Healthy: v.Healthy, DetectedFailureMode: v.DetectedFailureMode}
		if v.Err != "" {
			return result, fmt.Errorf("%s", v.Err)
		}
		return result, nil
	}
	p.unmatched = append(p.unmatched, req.ProbeRef)
	fallback := p.Fallback
	p.mu.Unlock()

	if fallback != nil {
		return fallback.Probe(req)
	}

	return entities.ProbeResult{
		Healthy:             false,
		DetectedFailureMode: fmt.Sprintf("replay: the recording holds no verdict for probe %q", req.ProbeRef),
	}, nil
}

func (p *Player) Unmatched() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.unmatched...)
}

type Difference struct {
	Field  string `json:"field"`
	Before any    `json:"before"`
	After  any    `json:"after"`
}

func (d Difference) String() string {
	return fmt.Sprintf("%s: %v -> %v", d.Field, d.Before, d.After)
}

func Compare(rec Recording, result entities.WorkflowResult, runErr error) []Difference {
	var diffs []Difference

	if before, after := joinTrace(rec.Trace), joinTrace(result.Trace); before != after {
		diffs = append(diffs, Difference{Field: "trace", Before: before, After: after})
	}
	if before, after := rec.Suspended, result.StatePointer != ""; before != after {
		diffs = append(diffs, Difference{Field: "suspended", Before: before, After: after})
	}

	failed := runErr != nil
	if rec.Failed != failed {
		diffs = append(diffs, Difference{Field: "failed", Before: rec.Failed, After: failed})
	} else if failed && rec.Error != runErr.Error() {
		diffs = append(diffs, Difference{Field: "error", Before: rec.Error, After: runErr.Error()})
	}

	seen := map[string]bool{}
	for key, before := range rec.Output {
		seen[key] = true
		after, present := result.Output[key]
		if !present {
			diffs = append(diffs, Difference{Field: "output." + key, Before: before, After: nil})
			continue
		}
		if !sameValue(before, after) {
			diffs = append(diffs, Difference{Field: "output." + key, Before: before, After: after})
		}
	}
	for key, after := range result.Output {
		if !seen[key] {
			diffs = append(diffs, Difference{Field: "output." + key, Before: nil, After: after})
		}
	}
	return diffs
}

func sameValue(a, b any) bool {
	ja, erra := json.Marshal(a)
	jb, errb := json.Marshal(b)
	if erra != nil || errb != nil {
		return fmt.Sprintf("%v", a) == fmt.Sprintf("%v", b)
	}
	return string(ja) == string(jb)
}

func joinTrace(steps []string) string {
	out := ""
	for i, s := range steps {
		if i > 0 {
			out += " -> "
		}
		out += s
	}
	if out == "" {
		return "(empty)"
	}
	return out
}

func cloneMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func (r *Recorder) ObserveExtraction(string) {}

func (r *Recorder) ObserveExtractionResult(req entities.ExtractionRequest, result entities.ExtractionResult) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.extractions = append(r.extractions, ExtractionRecord{
		SchemaRef:        req.SchemaRef,
		DomainID:         req.DomainID,
		RawText:          req.RawText,
		Success:          result.Success,
		Payload:          cloneMap(result.StructuredPayload),
		ModelDerived:     append([]string(nil), result.ModelDerived...),
		ValidationErrors: append([]string(nil), result.ValidationErrors...),
	})
}

func (p *Player) ExtractorFor(schemaRef string) func(rawText string) (map[string]any, error) {
	return func(string) (map[string]any, error) {
		p.mu.Lock()
		defer p.mu.Unlock()

		queued := p.extractions[schemaRef]
		if len(queued) == 0 {
			p.unmatched = append(p.unmatched, "extraction:"+schemaRef)
			return nil, fmt.Errorf("replay: the recording holds no extraction for schema %q", schemaRef)
		}
		rec := queued[0]
		if len(queued) > 1 {
			p.extractions[schemaRef] = queued[1:]
		}
		if !rec.Success {
			reason := "extraction failed"
			if len(rec.ValidationErrors) > 0 {
				reason = rec.ValidationErrors[0]
			}
			return nil, fmt.Errorf("%s", reason)
		}
		return cloneMap(rec.Payload), nil
	}
}
