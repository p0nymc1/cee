package main

import (
	"strings"
	"testing"

	"github.com/p0nymc1/cee/execution"
)

func entry(t *testing.T) (*execution.Engine, string) {
	t.Helper()
	router, engine := buildRuntime()
	match := router.Match("refunds", "customer wants a refund")
	if !match.Matched {
		t.Fatal("the refund intent should match")
	}
	return engine, match.EntryWorkflowRef
}

func TestSmallRefundSettlesItself(t *testing.T) {
	engine, ref := entry(t)
	if got := handle(engine, ref, "acct-100", 20); got != "paid" {
		t.Fatalf("expected a small refund to pay out, got %q", got)
	}
}

func TestLargeRefundParksInsteadOfFailing(t *testing.T) {
	engine, ref := entry(t)
	got := handle(engine, ref, "acct-100", 500)
	if !strings.HasPrefix(got, "parked") {
		t.Fatalf("expected the run to park for a manager, got %q", got)
	}
}

func TestParkedRefundResumesToPayment(t *testing.T) {
	engine, ref := entry(t)

	parked, err := engine.Run(ref, map[string]any{"account": "acct-100", "amount": 500.0})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if parked.StatePointer == "" {
		t.Fatal("expected a resume pointer")
	}

	resumed, err := engine.Resume(parked.StatePointer, map[string]any{"approved": true})
	if err != nil {
		t.Fatalf("resuming should succeed: %v", err)
	}
	if resumed.Output["account"] != "acct-100" {
		t.Fatalf("context from before the pause was lost: %v", resumed.Output)
	}
}

func TestClosedAccountIsCaughtBeforeAnyMoneyMoves(t *testing.T) {
	engine, ref := entry(t)

	result, err := engine.Run(ref, map[string]any{"account": "acct-991", "amount": 20.0})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Output["paid"] == true {
		t.Fatal("a refund against a closed account must never be paid")
	}
	why, _ := result.Output[execution.FailureReasonKey].(string)
	if !strings.Contains(why, "closed") {
		t.Fatalf("the hold should explain itself, got %q", why)
	}
}

func TestClosedAccountIsRefusedRegardlessOfAmount(t *testing.T) {
	engine, ref := entry(t)
	for _, amount := range []float64{1, 20, 500, 100000} {
		got := handle(engine, ref, "acct-991", amount)
		if !strings.HasPrefix(got, "held") {
			t.Fatalf("$%.0f on a closed account should be held, got %q", amount, got)
		}
	}
}
