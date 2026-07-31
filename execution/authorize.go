package execution

import "fmt"

const ResumedByKey = "cee.resumed_by"

type ResumeAttempt struct {
	Audience string

	Identity   string
	WorkflowID string
	StepID     string
	Reason     string
}

type Authorizer interface {
	Authorize(ResumeAttempt) (bool, string, error)
}

type AuthorizerFunc func(ResumeAttempt) (bool, string, error)

func (f AuthorizerFunc) Authorize(a ResumeAttempt) (bool, string, error) { return f(a) }

func (e *Engine) SetAuthorizer(a Authorizer) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.authorizer = a
}

func (e *Engine) currentAuthorizer() Authorizer {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.authorizer
}

type NotAuthorized struct {
	Audience   string
	Identity   string
	WorkflowID string
	Reason     string
}

func (e *NotAuthorized) Error() string {
	who := e.Identity
	if who == "" {
		who = "an unidentified caller"
	}
	msg := fmt.Sprintf("%s may not resume %q (audience %q)", who, e.WorkflowID, e.Audience)
	if e.Reason != "" {
		msg += ": " + e.Reason
	}
	return msg
}

func (e *Engine) authorize(state State, identity string) error {
	if state.Audience == "" {

		return nil
	}

	attempt := ResumeAttempt{
		Audience:   state.Audience,
		Identity:   identity,
		WorkflowID: state.WorkflowID,
		StepID:     state.StepID,
		Reason:     state.Reason,
	}

	authorizer := e.currentAuthorizer()
	if authorizer == nil {
		return &NotAuthorized{
			Audience: state.Audience, Identity: identity, WorkflowID: state.WorkflowID,
			Reason: "this suspension declares an audience but no Authorizer is configured; call Engine.SetAuthorizer",
		}
	}

	ok, reason, err := authorizer.Authorize(attempt)
	if err != nil {

		return &NotAuthorized{
			Audience: state.Audience, Identity: identity, WorkflowID: state.WorkflowID,
			Reason: "authorization check failed: " + err.Error(),
		}
	}
	if !ok {
		return &NotAuthorized{
			Audience: state.Audience, Identity: identity, WorkflowID: state.WorkflowID,
			Reason: reason,
		}
	}
	return nil
}
