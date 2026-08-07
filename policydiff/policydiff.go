// Package policydiff answers the question a rule change should have to answer
// before it ships: which of the decisions we already made would come out
// differently?
//
// It runs one dataset of historical inputs through two versions of a manifest
// and reports the difference. The engine is not involved beyond being run
// twice; determinism is what makes the comparison meaningful, because any
// difference is attributable to the manifest rather than to scheduling or to a
// live dependency having moved.
//
// A changed outcome and a changed explanation are counted separately. Fields
// the engine writes about itself (the cee.* keys) and the step trace describe
// how a decision was reached; every other output field is the decision. Folding
// them together overstates the blast radius of a change, which is the number a
// reviewer is most likely to act on.
package policydiff

import (
	"fmt"
	"sort"
	"strings"

	"github.com/p0nymc1/cee/bench"
	"github.com/p0nymc1/cee/execution"
	"github.com/p0nymc1/cee/intentrouter"
	"github.com/p0nymc1/cee/manifest"
	"github.com/p0nymc1/cee/registry"
	"github.com/p0nymc1/cee/replay"
	"github.com/p0nymc1/cee/stdlib"
)

// Classification says what kind of change an event saw.
type Classification int

const (
	Same Classification = iota
	// ExplanationChanged: the decision stands, but the reason recorded for it
	// or the path taken to it is different.
	ExplanationChanged
	// OutcomeChanged: a field that is not engine bookkeeping came out
	// differently. This is the number that matters.
	OutcomeChanged
)

func (c Classification) String() string {
	switch c {
	case OutcomeChanged:
		return "outcome"
	case ExplanationChanged:
		return "explanation"
	default:
		return "same"
	}
}

// Change is one historical input whose replay differed.
type Change struct {
	Index       int
	WorkflowRef string
	Context     map[string]any
	Class       Classification
	Differences []replay.Difference
}

// Report is the whole comparison.
type Report struct {
	Events       int
	Outcomes     []Change
	Explanations []Change
	BeforeErrors int
	AfterErrors  int
}

// Flipped is the headline: how many decisions came out differently.
func (r Report) Flipped() int { return len(r.Outcomes) }

// Clean reports whether nothing at all changed.
func (r Report) Clean() bool { return len(r.Outcomes) == 0 && len(r.Explanations) == 0 }

// Compare runs events through both manifests and reports the difference.
// Neither manifest may need Go hooks: this is for the no-code tier, where a
// policy is data and a reviewer can diff it. A manifest that needs hooks cannot
// be reconstructed from JSON alone, so it is rejected rather than silently
// compared against a partial domain.
func Compare(beforeManifest, afterManifest []byte, suite bench.Suite, std stdlib.Library) (Report, error) {
	beforeEngine, err := build(beforeManifest, std)
	if err != nil {
		return Report{}, fmt.Errorf("before: %w", err)
	}
	afterEngine, err := build(afterManifest, std)
	if err != nil {
		return Report{}, fmt.Errorf("after: %w", err)
	}

	report := Report{Events: len(suite.Events)}

	for i, ev := range suite.Events {
		beforeResult, beforeErr := beforeEngine.Run(ev.WorkflowRef, clone(ev.Context))
		afterResult, afterErr := afterEngine.Run(ev.WorkflowRef, clone(ev.Context))
		if beforeErr != nil {
			report.BeforeErrors++
		}
		if afterErr != nil {
			report.AfterErrors++
		}

		recording := replay.Recording{
			WorkflowID: ev.WorkflowRef,
			Input:      ev.Context,
			Trace:      beforeResult.Trace,
			Output:     beforeResult.Output,
			Suspended:  beforeResult.StatePointer != "",
			Failed:     beforeErr != nil,
		}
		if beforeErr != nil {
			recording.Error = beforeErr.Error()
		}

		diffs := replay.Compare(recording, afterResult, afterErr)
		if len(diffs) == 0 {
			continue
		}

		change := Change{
			Index: i, WorkflowRef: ev.WorkflowRef,
			Context: ev.Context, Differences: diffs,
			Class: classify(diffs),
		}
		if change.Class == OutcomeChanged {
			report.Outcomes = append(report.Outcomes, change)
		} else {
			report.Explanations = append(report.Explanations, change)
		}
	}

	return report, nil
}

// classify decides whether a set of differences represents a changed decision
// or only a changed explanation of the same decision.
func classify(diffs []replay.Difference) Classification {
	for _, d := range diffs {
		field := d.Field
		switch {
		case field == "trace":
			continue
		case strings.HasPrefix(field, "output.cee."):
			continue
		default:
			// suspended, failed, error, or any business output field.
			return OutcomeChanged
		}
	}
	return ExplanationChanged
}

func build(data []byte, std stdlib.Library) (*execution.Engine, error) {
	domain, err := manifest.Load(data, nil, std)
	if err != nil {
		return nil, err
	}
	engine := execution.NewEngine(nil)
	registry.NewRegistry(intentrouter.NewRouter(0.3), engine).RegisterDomain(*domain)
	return engine, nil
}

func clone(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// Text renders the report for a terminal.
func (r Report) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "replayed %d historical events against the proposed manifest\n", r.Events)
	fmt.Fprintf(&b, "  %d of %d decisions change\n", r.Flipped(), r.Events)

	if len(r.Outcomes) > 0 {
		b.WriteString("\n")
		for _, c := range r.Outcomes {
			fmt.Fprintf(&b, "  event %-4d %s\n", c.Index, describe(c.Context))
			for _, d := range c.Differences {
				if strings.HasPrefix(d.Field, "output.cee.") || d.Field == "trace" {
					continue
				}
				fmt.Fprintf(&b, "      %s\n", d)
			}
		}
	}

	if len(r.Explanations) > 0 {
		fmt.Fprintf(&b, "\n  %d more keep the same outcome; only the recorded reason or path changes\n",
			len(r.Explanations))
	}
	if r.BeforeErrors > 0 || r.AfterErrors > 0 {
		fmt.Fprintf(&b, "\n  runs that errored: %d before, %d after\n", r.BeforeErrors, r.AfterErrors)
	}
	return b.String()
}

// Markdown renders the report for a pull-request comment.
func (r Report) Markdown() string {
	var b strings.Builder

	if r.Clean() {
		fmt.Fprintf(&b, "### No decision changes\n\nReplayed **%d** historical events against the proposed manifest. Every one produced an identical outcome.\n", r.Events)
		return b.String()
	}

	fmt.Fprintf(&b, "### %d of %d past decisions would change\n\n", r.Flipped(), r.Events)

	if len(r.Outcomes) > 0 {
		b.WriteString("| # | input | change |\n|---|---|---|\n")
		for _, c := range r.Outcomes {
			var parts []string
			for _, d := range c.Differences {
				if strings.HasPrefix(d.Field, "output.cee.") || d.Field == "trace" {
					continue
				}
				parts = append(parts, fmt.Sprintf("`%s`: %s → %s",
					strings.TrimPrefix(d.Field, "output."), Display(d.Before), Display(d.After)))
			}
			fmt.Fprintf(&b, "| %d | %s | %s |\n", c.Index, describe(c.Context), strings.Join(parts, "<br>"))
		}
	}

	if len(r.Explanations) > 0 {
		fmt.Fprintf(&b, "\n%d more keep the same outcome; only the recorded reason or the path taken changes.\n",
			len(r.Explanations))
	}
	if r.BeforeErrors > 0 || r.AfterErrors > 0 {
		fmt.Fprintf(&b, "\nRuns that errored: %d before, %d after.\n", r.BeforeErrors, r.AfterErrors)
	}

	b.WriteString("\n<sub>Generated by <code>cee diff</code> — a deterministic replay, so every difference above is attributable to the manifest change.</sub>\n")
	return b.String()
}

// Display renders a field value for a human. A field that was absent on one
// side of the comparison is nil, and Go prints that as "<nil>", which reads as
// a bug rather than as "this field was not set".
func Display(v any) string {
	if v == nil {
		return "(none)"
	}
	return fmt.Sprint(v)
}

func describe(ctx map[string]any) string {
	keys := make([]string, 0, len(ctx))
	for k := range ctx {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%v", k, ctx[k]))
	}
	return strings.Join(parts, " ")
}
