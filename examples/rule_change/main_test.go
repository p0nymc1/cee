package main

import (
	"strings"
	"testing"
)

func TestTighteningTheLimitFlipsExactlyTheRefundsBetweenTheTwo(t *testing.T) {
	recordings, err := record("100")
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	changes, err := replayUnder("50", recordings)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}

	for _, c := range changes {
		if !c.Flipped {
			continue
		}
		if c.Amount <= 50 || c.Amount > 100 {
			t.Errorf("%s ($%.0f) flipped, but only refunds between $50 and $100 should",
				c.RefundID, c.Amount)
		}
		if c.Before != "paid" || c.After != "held" {
			t.Errorf("%s went %s -> %s, want paid -> held", c.RefundID, c.Before, c.After)
		}
	}
}

func TestARecordedProbeRefusalSurvivesTheRuleChange(t *testing.T) {
	recordings, err := record("100")
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	changes, err := replayUnder("50", recordings)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}

	flipped := map[string]bool{}
	for _, c := range changes {
		if c.Flipped {
			flipped[c.RefundID] = true
		}
	}

	var closedInRange []string
	for _, r := range history() {
		if closedAccounts[r.Account] && r.Amount > 50 && r.Amount <= 100 {
			closedInRange = append(closedInRange, r.ID)
		}
	}
	if len(closedInRange) == 0 {
		t.Fatal("the fixture must contain a closed account inside the flip range, or this proves nothing")
	}

	for _, id := range closedInRange {
		if flipped[id] {
			t.Errorf("%s flipped, but its account was already closed when recorded; "+
				"a replayed probe verdict must not be re-decided against today's world", id)
		}
	}
}

func TestAnUnchangedRuleFlipsNothing(t *testing.T) {
	recordings, err := record("100")
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	changes, err := replayUnder("100", recordings)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if len(changes) != 0 {
		t.Errorf("replaying the same rule reported %d differences; the engine is not reproducing itself: %v",
			len(changes), changes)
	}
}

func TestTheReportIsByteIdenticalOnEveryRun(t *testing.T) {
	first, err := run("100", "50")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	for i := 0; i < 5; i++ {
		again, err := run("100", "50")
		if err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		if again != first {
			t.Fatalf("run %d differed from the first; a regression diff that is not itself reproducible is worthless", i)
		}
	}
}

func TestTheReportSeparatesFlipsFromRewordedExplanations(t *testing.T) {
	report, err := run("100", "50")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(report, "decisions flip") {
		t.Error("the report must lead with how many decisions flip")
	}
	if !strings.Contains(report, "The outcome stands") {
		t.Error("a changed explanation is not a changed decision, and the report has to say so")
	}
	flipLines := strings.Count(report, "paid -> held")
	if flipLines == 0 {
		t.Error("the report lists no flips")
	}
	if strings.Contains(report, "held -> paid") {
		t.Error("tightening a limit cannot turn a held refund into a paid one")
	}
}
