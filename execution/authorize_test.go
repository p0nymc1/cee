package execution

import (
	"errors"
	"strings"
	"testing"
)

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

	if len(store.Pending()) != 1 {
		t.Fatal("a refused attempt must leave the run pending")
	}
}

func TestARefusalDoesNotConsumeThePointer(t *testing.T) {
	engine, store := approvalEngine(t, "finance-manager")
	engine.SetAuthorizer(AuthorizerFunc(func(a ResumeAttempt) (bool, string, error) {
		return a.Identity == "wei", "not a finance manager", nil
	}))

	parked, _ := engine.Run("payments.release", map[string]any{})

	for i := 0; i < 3; i++ {
		if _, err := engine.ResumeAs(parked.StatePointer, "mallory", nil); err == nil {
			t.Fatal("an unauthorised caller must not resume")
		}
	}
	if len(store.Pending()) != 1 {
		t.Fatal("the approval must survive unauthorised attempts")
	}

	result, err := engine.ResumeAs(parked.StatePointer, "wei", map[string]any{"approved": true})
	if err != nil {
		t.Fatalf("the authorised caller should succeed: %v", err)
	}
	if result.Output["paid"] != true {
		t.Fatalf("expected the run to complete, got %v", result.Output)
	}
}

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
