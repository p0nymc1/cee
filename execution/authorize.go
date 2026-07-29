package execution

import "fmt"

// Authorization closes the hole that made a resume pointer a bearer token.
//
// A pointer is unguessable, which stops someone finding one. It does nothing
// about someone who legitimately has one: forwarded email, a link pasted into
// a chat, a log line. Whoever holds it can approve, and the engine had no way
// to ask whether they should, nor to record who did.
//
// A suspension may therefore declare an audience -- an opaque, domain-defined
// name for who is allowed to answer it. The engine never interprets it; it
// hands the audience and the caller's claimed identity to an Authorizer the
// domain supplies, exactly as it hands a probe request to a Prober.
//
// Two rules make this worth having rather than decorative:
//
//   - It fails closed. A suspension that names an audience, on an engine with
//     no Authorizer attached, is refused. Silently accepting would make the
//     declaration a comment.
//   - A refusal does not consume the pointer. Otherwise anyone holding a link
//     could destroy a pending approval simply by being unauthorised, which
//     turns access control into a denial of service.

// ResumedByKey is the context key recording which identity resumed a run.
// Namespaced like the engine's other keys, and written on every authorised
// resume so the decision has an author in the trace, not just an outcome.
const ResumedByKey = "cee.resumed_by"

// ResumeAttempt is what an Authorizer is asked to rule on.
type ResumeAttempt struct {
	// Audience is what the suspension declared. Opaque to the engine.
	Audience string
	// Identity is who the caller claims to be, as passed to ResumeAs. The
	// engine does not authenticate it -- proving identity belongs to the
	// service in front of the engine, and pretending otherwise here would
	// invite treating an unverified string as proof.
	Identity   string
	WorkflowID string
	StepID     string
	Reason     string
}

// Authorizer decides whether an identity may answer a suspension.
type Authorizer interface {
	// Authorize returns whether the attempt is allowed, and when it is not, a
	// reason fit to show the caller.
	Authorize(ResumeAttempt) (bool, string, error)
}

// AuthorizerFunc adapts a plain function.
type AuthorizerFunc func(ResumeAttempt) (bool, string, error)

func (f AuthorizerFunc) Authorize(a ResumeAttempt) (bool, string, error) { return f(a) }

// SetAuthorizer attaches the domain's authorization policy. Without one, any
// suspension that declares an audience is refused.
func (e *Engine) SetAuthorizer(a Authorizer) { e.authorizer = a }

// NotAuthorized is returned when an identity may not answer a suspension.
// The pointer is untouched: the approval is still pending for whoever may
// actually give it.
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

// authorize rules on an attempt before the pointer is claimed.
func (e *Engine) authorize(state State, identity string) error {
	if state.Audience == "" {
		// Nothing was declared, so there is nothing to enforce. Workflows that
		// never name an audience behave exactly as before.
		return nil
	}

	attempt := ResumeAttempt{
		Audience:   state.Audience,
		Identity:   identity,
		WorkflowID: state.WorkflowID,
		StepID:     state.StepID,
		Reason:     state.Reason,
	}

	if e.authorizer == nil {
		return &NotAuthorized{
			Audience: state.Audience, Identity: identity, WorkflowID: state.WorkflowID,
			Reason: "this suspension declares an audience but no Authorizer is configured; call Engine.SetAuthorizer",
		}
	}

	ok, reason, err := e.authorizer.Authorize(attempt)
	if err != nil {
		// An authorizer that cannot reach its directory has not said yes.
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
