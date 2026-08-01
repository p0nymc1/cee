package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/p0nymc1/cee/entities"
	"github.com/p0nymc1/cee/execution"
	"github.com/p0nymc1/cee/intentrouter"
	"github.com/p0nymc1/cee/llminjector"
)

func TestMain(m *testing.M) {
	// buildRuntime reads the manifest by a repo-root-relative path; the tests
	// run from the package directory, so move to the root first.
	if err := os.Chdir(filepath.Join("..", "..")); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}

func runtime(t *testing.T) (*intentrouter.Router, *execution.Engine, *llminjector.Injector) {
	t.Helper()
	router, engine, injector, err := buildRuntime()
	if err != nil {
		t.Fatalf("buildRuntime: %v", err)
	}
	return router, engine, injector
}

func scannerCtx(f finding) map[string]any {
	return map[string]any{
		"finding_confidence": f.confidence,
		"autofixable":        f.autofixable,
		"is_incident_hotfix": f.isIncidentHotfix,
		"file_generated":     f.fileGenerated,
		"file":               f.file,
		"line":               f.line,
		"severity":           f.severity,
		"category":           f.category,
	}
}

func run(t *testing.T, engine *execution.Engine, ctx map[string]any) entities.WorkflowResult {
	t.Helper()
	result, err := engine.Run("codeaudit.triage", ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return result
}

func TestACriticalScannerFindingBlocksTheMerge(t *testing.T) {
	_, engine, _ := runtime(t)
	out := run(t, engine, scannerCtx(finding{
		severity: "critical", category: "injection", file: "internal/auth/login.go", confidence: 0.98,
	})).Output
	if !strings.Contains(out["disposition"].(string), "merge blocked") {
		t.Errorf("disposition = %v, want a merge block", out["disposition"])
	}
}

func TestAnIncidentHotfixIsNotAutoBlocked(t *testing.T) {
	_, engine, _ := runtime(t)
	out := run(t, engine, scannerCtx(finding{
		severity: "critical", category: "injection", file: "internal/auth/session.go",
		confidence: 0.97, isIncidentHotfix: true,
	})).Output
	if out["disposition"] != "held_for_human_review" {
		t.Fatalf("a correct finding on an incident hotfix must be held, got %v", out["disposition"])
	}
	if !strings.Contains(out[execution.FailureReasonKey].(string), "incident") {
		t.Errorf("the reason should name the incident hotfix, got %v", out[execution.FailureReasonKey])
	}
}

func TestAModelDerivedSeverityCannotAutoBlock(t *testing.T) {
	_, engine, injector := runtime(t)
	res := injector.Extract(entities.ExtractionRequest{
		SchemaRef: "codeaudit.finding_fields", DomainID: "codeaudit",
		RawText: "internal/api/handler.go:120 [critical] category=injection possibly exploitable",
	})
	if !res.Success {
		t.Fatalf("extraction failed: %v", res.ValidationErrors)
	}
	ctx := llminjector.ContextFrom(map[string]any{
		"finding_confidence": 0.9, "autofixable": false,
		"is_incident_hotfix": false, "file_generated": false,
	}, res)

	out := run(t, engine, ctx).Output
	if out["disposition"] != "held_for_human_review" {
		t.Fatalf("a model-derived critical must not auto-block, got %v", out["disposition"])
	}
	if !strings.Contains(out[execution.FailureReasonKey].(string), "not verified facts") {
		t.Errorf("the reason should be the unverified-severity refusal, got %v", out[execution.FailureReasonKey])
	}
}

func TestAScannerSeverityIsVerifiedAndIsNotRefused(t *testing.T) {
	// The same critical severity, but from the scanner rather than the model,
	// must pass require_verified and reach the gate. This is the whole point of
	// provenance: identical values, different trust.
	_, engine, _ := runtime(t)
	out := run(t, engine, scannerCtx(finding{
		severity: "critical", category: "injection", file: "internal/auth/login.go", confidence: 0.98,
	})).Output
	if out["disposition"] == "held_for_human_review" {
		t.Fatal("a verified scanner severity must not be refused by require_verified")
	}
}

func TestAnAutofixOnHandWrittenCodeIsApplied(t *testing.T) {
	_, engine, _ := runtime(t)
	out := run(t, engine, scannerCtx(finding{
		severity: "low", category: "style", file: "internal/util/format.go",
		autofixable: true, confidence: 0.95,
	})).Output
	if out["disposition"] != "autofix applied" {
		t.Errorf("disposition = %v, want autofix applied", out["disposition"])
	}
}

func TestAnAutofixOnGeneratedCodeIsRefused(t *testing.T) {
	_, engine, _ := runtime(t)
	out := run(t, engine, scannerCtx(finding{
		severity: "low", category: "style", file: "internal/pb/service.pb.go",
		autofixable: true, confidence: 0.95, fileGenerated: true,
	})).Output
	if out["disposition"] != "held_for_human_review" {
		t.Fatalf("autofixing generated code must be refused, got %v", out["disposition"])
	}
	if !strings.Contains(out[execution.FailureReasonKey].(string), "generated") {
		t.Errorf("the reason should name the generated file, got %v", out[execution.FailureReasonKey])
	}
}

func TestAnInfoFindingIsMerelyCommented(t *testing.T) {
	_, engine, _ := runtime(t)
	out := run(t, engine, scannerCtx(finding{
		severity: "info", category: "docs", file: "README.md", confidence: 0.99,
	})).Output
	if !strings.Contains(out["disposition"].(string), "commented") {
		t.Errorf("disposition = %v, want a non-blocking comment", out["disposition"])
	}
}

func TestALowConfidenceFindingIsHeld(t *testing.T) {
	_, engine, _ := runtime(t)
	out := run(t, engine, scannerCtx(finding{
		severity: "high", category: "tls", file: "internal/net/dial.go", confidence: 0.4,
	})).Output
	if out["disposition"] != "held_for_human_review" {
		t.Errorf("a low-confidence finding must be held, got %v", out["disposition"])
	}
}

func TestANonFindingMatchesNoIntent(t *testing.T) {
	router, _, _ := runtime(t)
	if router.Match("codeaudit", "the office coffee machine is out of beans again").Matched {
		t.Error("a non-finding must not match the review intent")
	}
}
