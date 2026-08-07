//go:build js && wasm

// Command ceewasm exposes the engine to a browser so the playground runs the
// real thing. Compiling the Go engine to WebAssembly is the point: a JavaScript
// re-implementation would be a second engine, and a second engine drifts from
// the first -- which for a project whose whole claim is determinism would be a
// self-inflicted wound. What the page runs is the same code that runs in a
// service.
//
// Every entry point takes and returns JSON strings. That keeps the bridge
// dumb, and it means the browser sees exactly the shapes the Go API produces.
package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"syscall/js"

	"github.com/p0nymc1/cee/bench"
	"github.com/p0nymc1/cee/execution"
	"github.com/p0nymc1/cee/intentrouter"
	"github.com/p0nymc1/cee/manifest"
	"github.com/p0nymc1/cee/policydiff"
	"github.com/p0nymc1/cee/registry"
	"github.com/p0nymc1/cee/stdlib"
)

func main() {
	js.Global().Set("ceeValidate", js.FuncOf(wrap(validate)))
	js.Global().Set("ceeRunSuite", js.FuncOf(wrap(runSuite)))
	js.Global().Set("ceeDiff", js.FuncOf(wrap(diff)))
	js.Global().Set("ceeReady", js.ValueOf(true))
	select {}
}

// wrap turns a panic into a returned error rather than tearing down the whole
// WebAssembly instance, so a malformed manifest cannot break the page until it
// is reloaded.
func wrap(fn func(args []js.Value) any) func(js.Value, []js.Value) any {
	return func(_ js.Value, args []js.Value) (result any) {
		defer func() {
			if r := recover(); r != nil {
				result = encode(map[string]any{"error": fmt.Sprintf("%v", r)})
			}
		}()
		return fn(args)
	}
}

func encode(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		return `{"error":"could not encode the result"}`
	}
	return string(data)
}

func fail(format string, a ...any) string {
	return encode(map[string]any{"error": fmt.Sprintf(format, a...)})
}

// validate statically checks a manifest and lists what it declares, so the page
// can show issues inline and offer the workflows in a picker.
func validate(args []js.Value) any {
	if len(args) < 1 {
		return fail("validate needs a manifest")
	}
	data := []byte(args[0].String())

	report := manifest.Validate(data, stdlib.Default())

	issues := make([]map[string]string, 0, len(report.Issues))
	for _, issue := range report.Issues {
		severity := "warning"
		if issue.Severity == manifest.Error {
			severity = "error"
		}
		issues = append(issues, map[string]string{"severity": severity, "message": issue.Message})
	}

	out := map[string]any{"ok": report.OK(), "issues": issues}

	// The workflow list only exists if the manifest actually loads.
	if domain, err := manifest.Load(data, nil, stdlib.Default()); err == nil {
		refs := make([]string, 0, len(domain.Workflows))
		for _, wf := range domain.Workflows {
			refs = append(refs, wf.WorkflowID)
		}
		out["workflows"] = refs
		out["domain"] = domain.Name
	} else {
		out["load_error"] = err.Error()
	}
	return encode(out)
}

// runSuite runs every event through the manifest and reports what each decided.
// This is the "watch it work" view: one row per input, with the path taken.
func runSuite(args []js.Value) any {
	if len(args) < 2 {
		return fail("runSuite needs a manifest and an event set")
	}

	domain, err := manifest.Load([]byte(args[0].String()), nil, stdlib.Default())
	if err != nil {
		return fail("%v", err)
	}
	suite, err := bench.ParseSuite([]byte(args[1].String()))
	if err != nil {
		return fail("%v", err)
	}

	engine := execution.NewEngine(nil)
	registry.NewRegistry(intentrouter.NewRouter(0.3), engine).RegisterDomain(*domain)

	rows := make([]map[string]any, 0, len(suite.Events))
	for i, ev := range suite.Events {
		result, runErr := engine.Run(ev.WorkflowRef, cloneContext(ev.Context))

		row := map[string]any{
			"index": i,
			"input": describe(ev.Context),
			"trace": result.Trace,
		}
		if runErr != nil {
			row["error"] = runErr.Error()
		}
		if result.StatePointer != "" {
			row["suspended"] = true
		}

		// Split what the workflow decided from what the engine recorded about
		// itself, so the decision is not buried in bookkeeping.
		decision := map[string]any{}
		var reason string
		for k, v := range result.Output {
			switch {
			case k == execution.FailureReasonKey:
				reason = fmt.Sprint(v)
			case strings.HasPrefix(k, "cee."):
			default:
				if _, carried := ev.Context[k]; !carried {
					decision[k] = v
				}
			}
		}
		row["decision"] = decision
		if reason != "" {
			row["reason"] = reason
		}
		rows = append(rows, row)
	}

	return encode(map[string]any{"events": len(suite.Events), "rows": rows})
}

// diff is the capability the playground exists to show: replay the same events
// against a changed manifest and report which decisions come out differently.
func diff(args []js.Value) any {
	if len(args) < 3 {
		return fail("diff needs a before manifest, an after manifest and an event set")
	}
	suite, err := bench.ParseSuite([]byte(args[2].String()))
	if err != nil {
		return fail("%v", err)
	}

	report, err := policydiff.Compare(
		[]byte(args[0].String()), []byte(args[1].String()), suite, stdlib.Default())
	if err != nil {
		return fail("%v", err)
	}

	outcomes := make([]map[string]any, 0, len(report.Outcomes))
	for _, c := range report.Outcomes {
		changes := make([]map[string]any, 0, len(c.Differences))
		for _, d := range c.Differences {
			if d.Field == "trace" || strings.HasPrefix(d.Field, "output.cee.") {
				continue
			}
			changes = append(changes, map[string]any{
				"field":  strings.TrimPrefix(d.Field, "output."),
				"before": policydiff.Display(d.Before),
				"after":  policydiff.Display(d.After),
			})
		}
		outcomes = append(outcomes, map[string]any{
			"index": c.Index, "input": describe(c.Context), "changes": changes,
		})
	}

	return encode(map[string]any{
		"events":       report.Events,
		"flipped":      report.Flipped(),
		"outcomes":     outcomes,
		"explanations": len(report.Explanations),
		"markdown":     report.Markdown(),
	})
}

func cloneContext(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func describe(ctx map[string]any) string {
	keys := make([]string, 0, len(ctx))
	for k := range ctx {
		keys = append(keys, k)
	}
	sortStrings(keys)

	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%v", k, ctx[k]))
	}
	return strings.Join(parts, " ")
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
