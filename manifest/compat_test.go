package manifest

import (
	"strings"
	"testing"

	"github.com/p0nymc1/cee/stdlib"
)

// entry_step_ref was the original name for the field that names an intent's
// target workflow. The name was wrong -- the value is a workflow_id, never a
// step_id -- and it is now entry_workflow_ref. Rule 3 of the normative
// handbook forbids removing a published JSON field, and manifests in the
// catalog already use the old one, so it keeps working and is reported as
// deprecated. These tests hold that bargain in place.

func compatManifest(entryField string) string {
	return `{
		"name": "compat",
		"intents": [
			{"node_id": "compat.go", "examples": ["do the thing"], ` + entryField + `}
		],
		"workflows": [{
			"workflow_id": "compat.wf",
			"entry_step_id": "a",
			"steps": [{"step_id": "a", "type": "leaf", "action_ref": "std.set", "with": {"fields": {"done": true}}}]
		}]
	}`
}

func TestDeprecatedEntryStepRefStillLoads(t *testing.T) {
	domain, err := Load([]byte(compatManifest(`"entry_step_ref": "compat.wf"`)), nil, stdlib.Default())
	if err != nil {
		t.Fatalf("a published manifest must keep loading: %v", err)
	}
	if len(domain.Intents) != 1 {
		t.Fatalf("expected one intent, got %d", len(domain.Intents))
	}
	// The old name always meant the entry workflow; only the name was wrong.
	if got := domain.Intents[0].EntryWorkflowRef; got != "compat.wf" {
		t.Fatalf("expected the deprecated field to resolve to compat.wf, got %q", got)
	}
}

func TestDeprecatedEntryStepRefIsValidButWarns(t *testing.T) {
	report := Validate([]byte(compatManifest(`"entry_step_ref": "compat.wf"`)), stdlib.Default())

	// Deprecated, not broken: it must still pass.
	if !report.OK() {
		t.Fatalf("a deprecated field must not fail validation, got:\n%s", report.String())
	}
	if !strings.Contains(report.String(), "deprecated") {
		t.Fatalf("expected a deprecation warning, got:\n%s", report.String())
	}
	if !strings.Contains(report.String(), "entry_workflow_ref") {
		t.Fatalf("the warning must name the replacement, got:\n%s", report.String())
	}
}

func TestCurrentEntryWorkflowRefWarnsAboutNothing(t *testing.T) {
	report := Validate([]byte(compatManifest(`"entry_workflow_ref": "compat.wf"`)), stdlib.Default())
	if !report.OK() {
		t.Fatalf("expected a clean report, got:\n%s", report.String())
	}
	for _, issue := range report.Issues {
		if issue.Severity == Warning {
			t.Fatalf("the current field name must produce no warning, got: %s", issue.Message)
		}
	}
}

// Setting both to the same value is redundant but unambiguous, so it is
// allowed -- it is what a careful author does mid-migration.
func TestBothNamesAgreeingIsAccepted(t *testing.T) {
	both := `"entry_workflow_ref": "compat.wf", "entry_step_ref": "compat.wf"`

	if _, err := Load([]byte(compatManifest(both)), nil, stdlib.Default()); err != nil {
		t.Fatalf("agreeing duplicates should load: %v", err)
	}
	if report := Validate([]byte(compatManifest(both)), stdlib.Default()); !report.OK() {
		t.Fatalf("agreeing duplicates should validate, got:\n%s", report.String())
	}
}

// Disagreeing is the one case that cannot be resolved without guessing which
// the author meant, and guessing is exactly what the handbook rules out.
func TestBothNamesDisagreeingIsRejected(t *testing.T) {
	both := `"entry_workflow_ref": "compat.wf", "entry_step_ref": "compat.other"`

	_, err := Load([]byte(compatManifest(both)), nil, stdlib.Default())
	if err == nil {
		t.Fatal("expected Load to refuse two conflicting entry references")
	}
	if !strings.Contains(err.Error(), "compat.wf") || !strings.Contains(err.Error(), "compat.other") {
		t.Fatalf("the error should show both values, got: %v", err)
	}

	report := Validate([]byte(compatManifest(both)), stdlib.Default())
	if report.OK() {
		t.Fatalf("expected Validate to reject the conflict, got:\n%s", report.String())
	}
}

func TestMissingBothNamesIsAnError(t *testing.T) {
	report := Validate([]byte(compatManifest(`"examples2": []`)), stdlib.Default())
	if report.OK() {
		t.Fatal("expected an intent with no entry reference to fail")
	}
	if !strings.Contains(report.String(), "entry_workflow_ref") {
		t.Fatalf("the error should ask for the current field name, got:\n%s", report.String())
	}
}
