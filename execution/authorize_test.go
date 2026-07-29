package execution

import (
	"errors"
	"strings"
	"testing"
)

// A resume pointer is unguessable, which stops someone finding one. It does
// nothing about someone who legitimately has one -- a forwarded email, a link
// in a chat, a log line. These cover the difference.

func approvalEngine(t *testing.T, audience string) (*Engine, *MemoryStore) {
	t.Helper()
	store := NewMemoryStore()
	engine := NewEngine(nil)
	engine.SetStore(store)
	engine.RegisterWorkflow(&Workflow{
		WorkflowID:  "payments.release",
		EntryStepID: "hold",
		Steps: map[string]Step{
			"hold": &LeafStep{
				StepID:    "hold",
				OnSuccess: "pay",
				Run: func(ctx map[string]any) (map[string]any, error) {
					if audience == "" {
						return Suspend("awaiting approval")
					}
					return SuspendFor("awaiting approval", audience)
				},
			},
			"pay": &LeafStep{
				StepID: "pay",
				Run: func(ctx map[string]any) (map[string]any, error) {
					return map[string]any{"paid": true}, nil
				},
			},
		},
	})
	return engine, store
}

// A workflow that names no audience keeps working exactly as before.
func TestAnUnaudiencedSuspensionResumesAsBefore(t *testing.T) {
	engine, _ := approvalEngine(t, "")

	parked, err := engine.Run("payments.release", map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	result, err := engine.Resume(parked.StatePointer, map[string]any{"approved": true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Output["paid"] != true {
		t.Fatalf("expected the run to complete, got %v", result.Output)
	}
}

// Failing closed is the point. A suspension that names an audience, on an
// engine with nobody to ask, must not resume -- otherwise the declaration is
// a comment.
func TestAnAudiencedSuspensionFailsClosedWithNoAuthorizer(t *testing.T) {
	engine, store := approvalEngine(t, "finance-manager")

	parked, _ := engine.Run("payments.release", map[string]any{})
	_, err := engine.ResumeAs(parked.StatePointer, "anyone", map[string]any{"approved": true})

	var denied *NotAuthorized
	if !errors.As(err, &denied) {
		t.Fatalf("expected refusal, got %v", err)
	}
	if !strings.Contains(err.Error(), "no Authorizer") {
		t.Fatalf("the refusal should say what is missing, got %q", err.Error())
	}
	// And the approval is still waiting for someone who may give it.
	if len(store.Pending()) != 1 {
		t.Fatal("a refused attempt must leave the run pending")
	}
}

// The denial-of-service that a naive implementation invites: anyone holding
// the link could burn a pending approval just by being unauthorised.
func TestARefusalDoesNotConsumeThePointer(t *testing.T) {
	engine, store := approvalEngine(t, "finance-manager")
	engine.SetAuthorizer(AuthorizerFunc(func(a ResumeAttempt) (bool, string, error) {
		return a.Identity == "wei", "not a finance manager", nil
	}))

	parked, _ := engine.Run("payments.release", map[string]any{})

	// Three unauthorised attempts.
	for i := 0; i < 3; i++ {
		if _, err := engine.ResumeAs(parked.StatePointer, "mallory", nil); err == nil {
			t.Fatal("an unauthorised caller must not resume")
		}
	}
	if len(store.Pending()) != 1 {
		t.Fatal("the approval must survive unauthorised attempts")
	}

	// The person who may actually approve still can.
	result, err := engine.ResumeAs(parked.StatePointer, "wei", map[string]any{"approved": true})
	if err != nil {
		t.Fatalf("the authorised caller should succeed: %v", err)
	}
	if result.Output["paid"] != true {
		t.Fatalf("expected the run to complete, got %v", result.Output)
	}
}

// A decision needs an author in the record, not just an outcome.
func TestTheResumingIdentityIsRecorded(t *testing.T) {
	engine, _ := approvalEngine(t, "finance-manager")
	engine.SetAuthorizer(AuthorizerFunc(func(ResumeAttempt) (bool, string, error) {
		return true, "", nil
	}))

	parked, _ := engine.Run("payments.release", map[string]any{})
	result, err := engine.ResumeAs(parked.StatePointer, "wei", map[string]any{"approved": true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Output[ResumedByKey] != "wei" {
		t.Fatalf("the run should record who answered, got %v", result.Output[ResumedByKey])
	}
}

// The authorizer is told what it needs to rule on, including why the run is
// parked -- "approve a $2 refund" and "approve a $2m transfer" are not the
// same decision.
func TestTheAuthorizerSeesTheWholeAttempt(t *testing.T) {
	engine, _ := approvalEngine(t, "finance-manager")

	var seen ResumeAttempt
	engine.SetAuthorizer(AuthorizerFunc(func(a ResumeAttempt) (bool, string, error) {
		seen = a
		return true, "", nil
	}))

	parked, _ := engine.Run("payments.release", map[string]any{})
	if _, err := engine.ResumeAs(parked.StatePointer, "wei", nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if seen.Audience != "finance-manager" || seen.Identity != "wei" {
		t.Fatalf("audience and identity should reach the authorizer, got %+v", seen)
	}
	if seen.WorkflowID != "payments.release" || seen.StepID != "hold" {
		t.Fatalf("the authorizer should know which run, got %+v", seen)
	}
	if seen.Reason != "awaiting approval" {
		t.Fatalf("the authorizer should know what is being approved, got %q", seen.Reason)
	}
}

// An authorizer that cannot reach its directory has not said yes.
func TestAnAuthorizerErrorIsARefusalNotAnApproval(t *testing.T) {
	engine, store := approvalEngine(t, "finance-manager")
	engine.SetAuthorizer(AuthorizerFunc(func(ResumeAttempt) (bool, string, error) {
		return false, "", errors.New("directory unreachable")
	}))

	parked, _ := engine.Run("payments.release", map[string]any{})
	_, err := engine.ResumeAs(parked.StatePointer, "wei", nil)

	var denied *NotAuthorized
	if !errors.As(err, &denied) {
		t.Fatalf("a broken authorizer must refuse, got %v", err)
	}
	if !strings.Contains(err.Error(), "directory unreachable") {
		t.Fatalf("the underlying failure should be visible, got %q", err.Error())
	}
	if len(store.Pending()) != 1 {
		t.Fatal("an outage must not destroy the pending approval")
	}
}

// Plain Resume carries no identity, so it cannot answer a suspension that
// asks who is calling.
func TestPlainResumeCannotAnswerAnAudiencedSuspension(t *testing.T) {
	engine, _ := approvalEngine(t, "finance-manager")
	engine.SetAuthorizer(AuthorizerFunc(func(a ResumeAttempt) (bool, string, error) {
		return a.Identity == "wei", "unknown caller", nil
	}))

	parked, _ := engine.Run("payments.release", map[string]any{})
	if _, err := engine.Resume(parked.StatePointer, nil); err == nil {
		t.Fatal("Resume passes no identity, so an audienced suspension must refuse it")
	}
}

// The audience is saved with the run, so the rule about who may answer
// survives a restart rather than being lost with the process that set it.
func TestTheAudienceIsSavedWithTheRun(t *testing.T) {
	engine, store := approvalEngine(t, "ir-oncall")
	if _, err := engine.Run("payments.release", map[string]any{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	pending := store.Pending()
	if len(pending) != 1 || pending[0].Audience != "ir-oncall" {
		t.Fatalf("the audience should be on the saved state, got %+v", pending)
	}
}
