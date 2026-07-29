// Package replay makes CEE's determinism usable rather than merely claimed.
//
// A deterministic engine can promise that the same inputs produce the same
// outputs. That promise is only worth something if you can cash it: take a
// run that happened in production, run it again, and see whether it still
// decides the same way. From there follows the thing no other architecture
// can offer -- change a rule, replay last quarter, and read off exactly which
// historical decisions would have flipped.
//
// The seam is execution.Prober. A workflow's actions are required to be
// deterministic (handbook rule 1.3 and the L2 contract), so the only place
// the outside world enters a run is a sandbox probe: it reads inventory,
// balances, market data -- things that will have moved on by the time you
// replay. Recorder wraps a real Prober and remembers every verdict; Player
// implements Prober and answers from that record. Neither requires a change
// to the engine.
//
// A replay that diverges is therefore a finding, not a bug in this package.
// It means either a rule changed -- which is the whole point -- or an action
// consulted something it should not have, which is a violation the engine
// could not otherwise detect.
package replay

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/p0nymc1/cee/entities"
)

// ProbeVerdict is one answer the outside world gave during a run.
type ProbeVerdict struct {
	ProbeRef            string `json:"probe_ref"`
	DomainID            string `json:"domain_id,omitempty"`
	Healthy             bool   `json:"healthy"`
	DetectedFailureMode string `json:"detected_failure_mode,omitempty"`
	// Err is the probe's transport-level failure, if any. Recorded as text
	// because a replay needs to reproduce the outcome, not the error value.
	Err string `json:"error,omitempty"`
}

// Recording is one execution captured in enough detail to run again.
// It is plain JSON so a run can be kept next to the incident it belongs to.
type Recording struct {
	WorkflowID string         `json:"workflow_id"`
	Input      map[string]any `json:"input"`
	Probes     []ProbeVerdict `json:"probes,omitempty"`
	Trace      []string       `json:"trace"`
	Output     map[string]any `json:"output"`
	// Suspended records whether the run parked. The pointer itself is not
	// kept: it is freshly minted every time, so comparing it would report a
	// difference on every replay of a perfectly reproducible run.
	Suspended  bool      `json:"suspended"`
	Failed     bool      `json:"failed"`
	Error      string    `json:"error,omitempty"`
	RecordedAt time.Time `json:"recorded_at"`
}

// Recorder wraps the real sandbox and remembers what it answered.
type Recorder struct {
	mu       sync.Mutex
	inner    Prober
	verdicts []ProbeVerdict
}

// Prober is execution.Prober, restated so this package does not import
// execution and can stay a leaf next to scorecard.
type Prober interface {
	Probe(entities.ProbeRequest) (entities.ProbeResult, error)
}

// NewRecorder wraps a Prober. inner may be nil for workflows that gate on
// nothing, in which case a probe request is a configuration error and is
// recorded as such rather than silently passing.
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

// Finish packages the run. Pass the WorkflowResult and error exactly as
// Engine.Run returned them, so a run that failed is as replayable as one that
// succeeded -- failures are usually the ones worth replaying.
func (r *Recorder) Finish(workflowID string, input map[string]any, result entities.WorkflowResult, runErr error) Recording {
	r.mu.Lock()
	defer r.mu.Unlock()

	rec := Recording{
		WorkflowID: workflowID,
		Input:      cloneMap(input),
		Probes:     append([]ProbeVerdict(nil), r.verdicts...),
		Trace:      append([]string(nil), result.Trace...),
		Output:     cloneMap(result.Output),
		Suspended:  result.StatePointer != "",
		RecordedAt: time.Now().UTC(),
	}
	if runErr != nil {
		rec.Failed = true
		rec.Error = runErr.Error()
	}
	return rec
}

// Player answers probes from a recording instead of from the world.
//
// Lookup is by probe reference rather than by position, so a replay whose
// path changed still gets sensible answers for the probes it does reach --
// the useful behaviour when you are deliberately changing a rule to see what
// moves.
type Player struct {
	mu        sync.Mutex
	byRef     map[string][]ProbeVerdict
	unmatched []string
	// Fallback answers probes the recording never saw. Leave it nil to have
	// those reported instead of silently invented.
	Fallback Prober
}

func NewPlayer(rec Recording) *Player {
	byRef := make(map[string][]ProbeVerdict, len(rec.Probes))
	for _, v := range rec.Probes {
		byRef[v.ProbeRef] = append(byRef[v.ProbeRef], v)
	}
	return &Player{byRef: byRef}
}

func (p *Player) Probe(req entities.ProbeRequest) (entities.ProbeResult, error) {
	p.mu.Lock()
	queued := p.byRef[req.ProbeRef]
	if len(queued) > 0 {
		v := queued[0]
		// Consume repeats in order, then reuse the last answer: a step reached
		// more times than it was recorded still gets that probe's verdict.
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
	// Refusing beats guessing. A verdict this recording never captured cannot
	// be reconstructed, and inventing "healthy" would quietly let a replay
	// execute something the original never did.
	return entities.ProbeResult{
		Healthy:             false,
		DetectedFailureMode: fmt.Sprintf("replay: the recording holds no verdict for probe %q", req.ProbeRef),
	}, nil
}

// Unmatched lists probes the replay asked for that the recording did not
// hold. A non-empty result means the replayed path went somewhere the
// original never did.
func (p *Player) Unmatched() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.unmatched...)
}

// Difference is one way a replay departed from what was recorded.
type Difference struct {
	Field  string `json:"field"`
	Before any    `json:"before"`
	After  any    `json:"after"`
}

func (d Difference) String() string {
	return fmt.Sprintf("%s: %v -> %v", d.Field, d.Before, d.After)
}

// Compare reports how a replayed run departs from its recording.
//
// An empty result is the determinism claim, cashed: same inputs, same path,
// same answer. A non-empty result is the regression report -- which decisions
// a rule change would have altered.
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

	// Output keys, both directions, so a field that stopped being produced is
	// as visible as one whose value moved.
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

// sameValue compares through JSON so a recording that has been round-tripped
// to disk -- where every number became a float64 -- still compares equal to a
// live run that produced ints.
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
