// Command code_audit reimplements the loop an AI pull-request review agent runs
// -- the pattern of CodeRabbit, GitHub Copilot code review, and Qodo PR-Agent --
// on CEE, so that the model reasons at the edge and the engine decides.
//
// Those agents send a diff (and often a static-analysis run) to a model that
// both finds the issue and decides what to do about it: comment, request
// changes, block the merge, or apply a fix. The decision is the model's, made
// fresh on every pull request, and the dangerous case is not a wrong finding
// but a right one whose action is catastrophic -- auto-blocking the hotfix that
// ends an incident, or rewriting generated code that the next build overwrites.
//
// CEE keeps the finding but takes back the decision. The model's only job is
// extraction: turning a free-text finding into structured fields, which are
// stamped model-derived. A deterministic workflow classifies severity, and:
//
//   - refuses to auto-block on a model-derived severity (std.require_verified) --
//     an extractor mislabelling "info" as "critical" cannot gate a merge;
//   - rehearses the blast radius of the action before taking it (a sandbox
//     probe) -- blocking an incident hotfix, or autofixing generated code,
//     routes to a human instead of executing;
//   - falls through to a human on low scanner confidence.
//
// A scanner finding carries verified fields; a model-raised finding carries
// model-derived ones, and only the difference in provenance decides whether an
// automated block is allowed. Run it with `go run ./examples/code_audit`.
package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/p0nymc1/cee/diagnostics"
	"github.com/p0nymc1/cee/entities"
	"github.com/p0nymc1/cee/execution"
	"github.com/p0nymc1/cee/intentrouter"
	"github.com/p0nymc1/cee/llminjector"
	"github.com/p0nymc1/cee/manifest"
	"github.com/p0nymc1/cee/registry"
	"github.com/p0nymc1/cee/sandbox"
	"github.com/p0nymc1/cee/stdlib"
)

const manifestPath = "examples/manifests/code-audit.json"

func blockingSeverity(sev string) bool { return sev == "high" || sev == "critical" }

func buildRuntime() (*intentrouter.Router, *execution.Engine, *llminjector.Injector, error) {
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("reading %s: %w", manifestPath, err)
	}

	router := intentrouter.NewRouter(0.3)
	sb := sandbox.NewSandbox()
	engine := execution.NewEngine(sb)
	injector := llminjector.NewInjector()

	// The edge model's only job: turn a free-text finding into structured
	// fields. There is no live model here, so a parser stands in for it -- but
	// what matters is that the injector stamps every field it returns
	// model-derived, exactly as it would for a real extractor.
	injector.RegisterSchema("codeaudit.finding_fields",
		llminjector.Schema{
			"file":     llminjector.FieldString,
			"line":     llminjector.FieldFloat64,
			"severity": llminjector.FieldString,
			"category": llminjector.FieldString,
		},
		parseFinding,
	)

	// The blast-radius probe: the point of the whole exercise. It runs before
	// the gate action, read-only, and refuses the actions that would do more
	// harm than the finding.
	sb.RegisterProbe("codeaudit.assess_action_blast_radius", func(ctx map[string]any) (bool, string, error) {
		action, _ := ctx["gate_action"].(string)
		switch action {
		case "block_merge":
			if truthy(ctx["is_incident_hotfix"]) {
				return false, "the pull request is the hotfix for an active incident; blocking it keeps the fix from shipping", nil
			}
			return true, "", nil
		case "apply_autofix":
			if truthy(ctx["file_generated"]) {
				return false, fmt.Sprintf("%v is generated; an autofix there is overwritten on the next build and hides the real source", ctx["file"]), nil
			}
			return true, "", nil
		case "comment":
			return true, "", nil
		}
		return false, fmt.Sprintf("unknown gate action %q", action), nil
	})

	hooks := manifest.Hooks{
		"codeaudit.classify": func(ctx map[string]any) (map[string]any, error) {
			severity, _ := ctx["severity"].(string)
			if severity == "" {
				return nil, fmt.Errorf("finding carries no severity to classify")
			}
			blocking := blockingSeverity(severity)
			autofixable := truthy(ctx["autofixable"])

			action := "comment"
			switch {
			case blocking:
				action = "block_merge"
			case autofixable:
				action = "apply_autofix"
			}
			return map[string]any{
				"is_blocking": blocking,
				"needs_gate":  blocking || autofixable,
				"gate_action": action,
			}, nil
		},

		"codeaudit.apply_gate": func(ctx map[string]any) (map[string]any, error) {
			action, _ := ctx["gate_action"].(string)
			file, _ := ctx["file"].(string)
			switch action {
			case "block_merge":
				return map[string]any{"disposition": "merge blocked; changes requested", "executed": "block_merge on " + file}, nil
			case "apply_autofix":
				return map[string]any{"disposition": "autofix applied", "executed": "apply_autofix on " + file}, nil
			}
			return nil, fmt.Errorf("apply_gate reached with a non-gating action %q", action)
		},

		"codeaudit.post_comment": func(ctx map[string]any) (map[string]any, error) {
			return map[string]any{"disposition": "commented (non-blocking)"}, nil
		},
	}

	domain, err := manifest.Load(data, hooks, stdlib.Default())
	if err != nil {
		return nil, nil, nil, fmt.Errorf("loading manifest: %w", err)
	}
	registry.NewRegistry(router, engine).RegisterDomain(*domain)
	return router, engine, injector, nil
}

// parseFinding is the stand-in extractor. A finding reads
// "path/to/file.go:42 [severity] category=name message", and every field it
// returns is treated as model-derived by the injector.
func parseFinding(raw string) (map[string]any, error) {
	fields := strings.Fields(raw)
	if len(fields) < 3 {
		return nil, fmt.Errorf("finding text is too short to parse")
	}

	loc := fields[0]
	colon := strings.LastIndex(loc, ":")
	if colon < 0 {
		return nil, fmt.Errorf("finding has no file:line location")
	}
	line, err := strconv.Atoi(loc[colon+1:])
	if err != nil {
		return nil, fmt.Errorf("finding line %q is not a number", loc[colon+1:])
	}

	severity := strings.Trim(fields[1], "[]")
	category := "unknown"
	for _, f := range fields[2:] {
		if strings.HasPrefix(f, "category=") {
			category = strings.TrimPrefix(f, "category=")
		}
	}
	return map[string]any{
		"file":     loc[:colon],
		"line":     float64(line),
		"severity": severity,
		"category": category,
	}, nil
}

type finding struct {
	summary          string // for intent routing
	source           string // "scanner" (verified) or "llm" (extracted, model-derived)
	raw              string // free-text finding, for the llm source
	file             string
	line             float64
	severity         string
	category         string
	autofixable      bool
	confidence       float64
	isIncidentHotfix bool
	fileGenerated    bool
	note             string
}

func main() {
	router, engine, injector, err := buildRuntime()
	if err != nil {
		fmt.Fprintln(os.Stderr, "setup failed:", err)
		os.Exit(1)
	}

	diag := diagnostics.NewRecorder()
	router.SetObserver(diag)
	engine.SetObserver(diag)

	for _, f := range feed() {
		diag.ObserveRun()
		handle(router, engine, injector, f)
	}

	fmt.Printf("\n%s\n", diag.Report())
}

func handle(router *intentrouter.Router, engine *execution.Engine, injector *llminjector.Injector, f finding) {
	fmt.Printf("\nPR FINDING  %s\n", f.summary)
	fmt.Printf("            source=%s  (%s)\n", f.source, f.note)

	match := router.Match("codeaudit", f.summary)
	if !match.Matched {
		fmt.Printf("  -> no review intent matched (best score %.2f); would fall through to extraction\n", match.Confidence)
		return
	}

	metadata := map[string]any{
		"finding_confidence": f.confidence,
		"autofixable":        f.autofixable,
		"is_incident_hotfix": f.isIncidentHotfix,
		"file_generated":     f.fileGenerated,
	}

	var ctx map[string]any
	switch f.source {
	case "scanner":
		// Scanner fields are facts, carried verbatim and not model-derived.
		ctx = metadata
		ctx["file"] = f.file
		ctx["line"] = f.line
		ctx["severity"] = f.severity
		ctx["category"] = f.category
	case "llm":
		// A model-raised finding: extract, and let the injector stamp the
		// fields model-derived so require_verified can see they are guesses.
		result := injector.Extract(entities.ExtractionRequest{
			SchemaRef: "codeaudit.finding_fields", DomainID: "codeaudit", RawText: f.raw,
		})
		if !result.Success {
			fmt.Printf("  -> extraction failed: %v\n", result.ValidationErrors)
			return
		}
		ctx = llminjector.ContextFrom(metadata, result)
	default:
		fmt.Printf("  -> unknown finding source %q\n", f.source)
		return
	}

	result, err := engine.Run(match.EntryWorkflowRef, ctx)
	if err != nil {
		fmt.Printf("  -> halted: %v\n", err)
		return
	}

	fmt.Printf("  -> %v\n", result.Output["disposition"])
	if executed, ok := result.Output["executed"]; ok {
		fmt.Printf("     action: %v\n", executed)
	} else if why, ok := result.Output[execution.FailureReasonKey]; ok {
		// Only meaningful when we did not act: it says why the run was held.
		// On a successful gate the reason field is just the branch artifact of
		// std.require routing to the gate path, so it is not shown.
		fmt.Printf("     because: %v\n", why)
	}
	fmt.Printf("     trace: %v\n", result.Trace)
}

func feed() []finding {
	return []finding{
		{
			summary: "review this scanner result on the diff", source: "scanner",
			file: "internal/auth/login.go", line: 42, severity: "critical", category: "injection",
			confidence: 0.98,
			note:       "real SQL injection on an ordinary path — block it",
		},
		{
			summary: "audit this pull request change for security issues", source: "scanner",
			file: "internal/auth/session.go", line: 88, severity: "critical", category: "injection",
			confidence: 0.97, isIncidentHotfix: true,
			note: "same severity, but this PR is the hotfix ending a live incident",
		},
		{
			summary: "review this code finding in the pull request", source: "llm",
			raw:        "internal/api/handler.go:120 [critical] category=injection model thinks this concatenation is exploitable",
			confidence: 0.9,
			note:       "the model guessed 'critical' from prose — a guess must not auto-block",
		},
		{
			summary: "triage this static analysis finding", source: "scanner",
			file: "internal/util/format.go", line: 12, severity: "low", category: "style",
			autofixable: true, confidence: 0.95,
			note: "trivial style issue on hand-written code — safe to autofix",
		},
		{
			summary: "triage this static analysis finding", source: "scanner",
			file: "internal/pb/service.pb.go", line: 3001, severity: "low", category: "style",
			autofixable: true, confidence: 0.95, fileGenerated: true,
			note: "same autofix, but the file is generated",
		},
		{
			summary: "check this scanner result on the diff", source: "scanner",
			file: "README.md", line: 5, severity: "info", category: "docs",
			confidence: 0.99,
			note:       "informational — a comment is enough",
		},
		{
			summary: "audit this pull request change for security issues", source: "scanner",
			file: "internal/net/dial.go", line: 61, severity: "high", category: "tls",
			confidence: 0.4,
			note:       "high severity but the scanner is unsure — no auto-action on a maybe",
		},
		{
			summary: "the office coffee machine is out of beans again", source: "scanner",
			file: "n/a", line: 0, severity: "info", category: "none", confidence: 1,
			note: "not a code finding at all — no intent matches",
		},
	}
}

func truthy(v any) bool { b, _ := v.(bool); return b }
