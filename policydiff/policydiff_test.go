package policydiff_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/p0nymc1/cee/bench"
	"github.com/p0nymc1/cee/policydiff"
	"github.com/p0nymc1/cee/stdlib"
)

// refundManifest is a threshold policy: at or under the limit it auto-approves,
// over it the breaker routes to a manager.
func refundManifest(limit int) []byte {
	return []byte(fmt.Sprintf(`{
      "name": "refunds",
      "policies": [{"policy_id": "to_manager", "fallback_step_ref": "manager"}],
      "workflows": [{
        "workflow_id": "refunds.evaluate",
        "entry_step_id": "check",
        "steps": [
          {"step_id": "check", "type": "leaf", "action_ref": "std.require",
           "with": {"field": "amount", "op": "lte", "value": %d},
           "circuit_breaker_policy_ref": "to_manager", "on_success": "approve"},
          {"step_id": "approve", "type": "leaf", "action_ref": "std.set",
           "with": {"fields": {"disposition": "auto_approved"}}},
          {"step_id": "manager", "type": "leaf", "action_ref": "std.set",
           "with": {"fields": {"disposition": "manager_review"}}}
        ]
      }]
    }`, limit))
}

func suite(amounts ...float64) bench.Suite {
	s := bench.Suite{PluginName: "refunds"}
	for _, a := range amounts {
		s.Events = append(s.Events, bench.Event{
			WorkflowRef: "refunds.evaluate",
			Context:     map[string]any{"amount": a},
		})
	}
	return s
}

func compare(t *testing.T, before, after []byte, s bench.Suite) policydiff.Report {
	t.Helper()
	report, err := policydiff.Compare(before, after, s, stdlib.Default())
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	return report
}

func TestTighteningALimitFlipsOnlyTheDecisionsBetweenTheTwo(t *testing.T) {
	report := compare(t, refundManifest(100), refundManifest(50), suite(20, 80, 250))

	if report.Flipped() != 1 {
		t.Fatalf("flipped = %d, want 1 (only the 80 sits between 50 and 100): %+v",
			report.Flipped(), report.Outcomes)
	}
	if got := report.Outcomes[0].Context["amount"]; got != 80.0 {
		t.Errorf("the flipped event was amount=%v, want 80", got)
	}
}

func TestAnUnchangedManifestReportsNothing(t *testing.T) {
	report := compare(t, refundManifest(100), refundManifest(100), suite(20, 80, 250))
	if !report.Clean() {
		t.Errorf("comparing a manifest against itself must report no change, got %+v", report)
	}
}

func TestAChangedReasonIsNotCountedAsAChangedDecision(t *testing.T) {
	// The 250 is over both limits, so it is held either way -- but the failure
	// reason the engine records names the threshold, so it differs. That is an
	// explanation change, and counting it as a flip would overstate the blast
	// radius of the change.
	report := compare(t, refundManifest(100), refundManifest(50), suite(250))

	if report.Flipped() != 0 {
		t.Fatalf("a refund held under both limits did not change decision, got %d flips", report.Flipped())
	}
	if len(report.Explanations) != 1 {
		t.Fatalf("the changed reason should be reported separately, got %+v", report.Explanations)
	}
}

func TestTheHeadlineCountsOutcomesNotDifferences(t *testing.T) {
	// 20 unchanged, 80 flips, 250 changes only its reason.
	report := compare(t, refundManifest(100), refundManifest(50), suite(20, 80, 250))

	if report.Flipped() != 1 || len(report.Explanations) != 1 {
		t.Fatalf("want 1 outcome and 1 explanation change, got %d and %d",
			report.Flipped(), len(report.Explanations))
	}
	text := report.Text()
	if !strings.Contains(text, "1 of 3 decisions change") {
		t.Errorf("the headline should be the outcome count: %s", text)
	}
}

func TestMarkdownNamesTheFieldAndBothValues(t *testing.T) {
	md := compare(t, refundManifest(100), refundManifest(50), suite(80)).Markdown()

	for _, want := range []string{"1 of 1", "disposition", "auto_approved", "manager_review", "amount=80"} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown missing %q:\n%s", want, md)
		}
	}
}

func TestMarkdownSaysSoWhenNothingChanges(t *testing.T) {
	md := compare(t, refundManifest(100), refundManifest(100), suite(20, 250)).Markdown()
	if !strings.Contains(md, "No decision changes") {
		t.Errorf("an unchanged policy should say so plainly:\n%s", md)
	}
}

func TestTheSameComparisonIsReproducible(t *testing.T) {
	// A regression diff that is not itself reproducible is worthless.
	first := compare(t, refundManifest(100), refundManifest(50), suite(20, 80, 250, 61, 99)).Markdown()
	for i := 0; i < 10; i++ {
		if again := compare(t, refundManifest(100), refundManifest(50), suite(20, 80, 250, 61, 99)).Markdown(); again != first {
			t.Fatalf("run %d differed from the first", i)
		}
	}
}

func TestAManifestNeedingGoHooksIsRejected(t *testing.T) {
	withHook := []byte(`{
      "name": "x",
      "workflows": [{"workflow_id":"x.w","entry_step_id":"s","steps":[
        {"step_id":"s","type":"leaf","action_ref":"x.custom_go_hook"}]}]
    }`)
	_, err := policydiff.Compare(withHook, withHook, suite(1), stdlib.Default())
	if err == nil {
		t.Fatal("a manifest whose action needs a Go hook cannot be compared from JSON alone; it must be refused")
	}
}
