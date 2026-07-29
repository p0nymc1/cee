// Package httpapi puts an HTTP surface in front of an engine.
//
// It is a handler, not a server. CEE is a library that lives inside a service
// you already have, and that stays true here: New returns an http.Handler to
// mount wherever you want, under whatever middleware you already run. The
// standalone binary is a convenience for trying it, not the deployment model.
//
// Three decisions in here are security decisions rather than API taste:
//
//   - A resume pointer travels in the request body, never in the path. A
//     pointer in a URL ends up in access logs, proxy logs, browser history and
//     Referer headers, and it authorises an approval. Path parameters are
//     more idiomatic REST and would leak a credential by design.
//   - Identity comes from the request, and the engine is told who is calling.
//     Proving identity is this layer's job, not the engine's; the engine only
//     enforces what a suspension declared about who may answer.
//   - It fails closed. Without an Identify function nothing is served unless
//     AllowAnonymous was set deliberately, so an unconfigured deployment is
//     not an open one.
package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/p0nymc1/cee/entities"
	"github.com/p0nymc1/cee/execution"
)

// DefaultMaxBodyBytes bounds a request body. Generous for a workflow context,
// small enough that an unbounded upload cannot exhaust memory.
const DefaultMaxBodyBytes = 1 << 20 // 1 MiB

// PendingLister is the optional slice of a Store that can enumerate parked
// runs. filestore.Store and execution.MemoryStore both satisfy it, though
// with different signatures, so each is adapted at the call site.
type PendingLister interface {
	Pending() ([]execution.State, error)
}

// Config assembles the handler.
type Config struct {
	Engine *execution.Engine

	// Identify authenticates the caller and returns who they are. The string
	// is passed to Engine.ResumeAs, where a suspension's audience is enforced
	// against it. Returning an error rejects the request.
	//
	// Whatever this reads -- a verified JWT, a session, a mutual-TLS subject
	// -- must actually be authenticated. The engine treats the result as
	// established fact, so a function that trusts a plain header hands out
	// approval rights to anyone who can set one.
	Identify func(*http.Request) (string, error)

	// AllowAnonymous serves requests with no identity. Every audienced
	// suspension will then be refused by the engine, which is the point:
	// this is for local development, not for a deployment that simply has
	// not got round to authentication.
	AllowAnonymous bool

	// Pending enables the operator listing when set.
	Pending PendingLister

	MaxBodyBytes int64
}

type api struct {
	cfg Config
}

// New builds the handler. It refuses a configuration that would serve
// unauthenticated traffic without saying so.
func New(cfg Config) (http.Handler, error) {
	if cfg.Engine == nil {
		return nil, fmt.Errorf("httpapi: an Engine is required")
	}
	if cfg.Identify == nil && !cfg.AllowAnonymous {
		return nil, fmt.Errorf(
			"httpapi: set Identify, or set AllowAnonymous to serve without authentication on purpose")
	}
	if cfg.MaxBodyBytes <= 0 {
		cfg.MaxBodyBytes = DefaultMaxBodyBytes
	}

	a := &api{cfg: cfg}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/run", a.run)
	mux.HandleFunc("POST /v1/resume", a.resume)
	mux.HandleFunc("GET /v1/pending", a.pending)
	return mux, nil
}

// runRequest starts a workflow.
type runRequest struct {
	Workflow string         `json:"workflow"`
	Context  map[string]any `json:"context"`
}

// resumeRequest answers a suspended one. The pointer is in the body on
// purpose -- see the package comment.
type resumeRequest struct {
	Pointer    string         `json:"pointer"`
	Resolution map[string]any `json:"resolution"`
}

// runResponse is the shape both endpoints return.
//
// Status separates what the transport did from what the workflow did. A run
// the engine carried to a conclusion is a successful request even when the
// conclusion was "abandoned": 500 would say the service is broken, which it
// is not.
type runResponse struct {
	Status       string         `json:"status"` // completed | suspended | failed
	Output       map[string]any `json:"output,omitempty"`
	Trace        []string       `json:"trace,omitempty"`
	StatePointer string         `json:"state_pointer,omitempty"`
	Reason       string         `json:"reason,omitempty"`
}

type pendingEntry struct {
	Pointer  string `json:"pointer"`
	Workflow string `json:"workflow"`
	Step     string `json:"step"`
	Reason   string `json:"reason"`
	Audience string `json:"audience,omitempty"`
}

func (a *api) run(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.identify(w, r); !ok {
		return
	}
	var req runRequest
	if !a.decode(w, r, &req) {
		return
	}
	if req.Workflow == "" {
		writeError(w, http.StatusBadRequest, "workflow is required")
		return
	}

	result, err := a.cfg.Engine.Run(req.Workflow, req.Context)
	a.writeRun(w, result, err)
}

func (a *api) resume(w http.ResponseWriter, r *http.Request) {
	identity, ok := a.identify(w, r)
	if !ok {
		return
	}
	var req resumeRequest
	if !a.decode(w, r, &req) {
		return
	}
	if req.Pointer == "" {
		writeError(w, http.StatusBadRequest, "pointer is required")
		return
	}

	result, err := a.cfg.Engine.ResumeAs(req.Pointer, identity, req.Resolution)

	// Authorization is the one failure that must be distinguishable, so a
	// caller knows to get the right person rather than to retry.
	var denied *execution.NotAuthorized
	if errors.As(err, &denied) {
		writeError(w, http.StatusForbidden, denied.Error())
		return
	}
	a.writeRun(w, result, err)
}

func (a *api) pending(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.identify(w, r); !ok {
		return
	}
	if a.cfg.Pending == nil {
		writeError(w, http.StatusNotFound, "no pending listing is configured")
		return
	}

	states, err := a.cfg.Pending.Pending()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not list pending runs")
		return
	}

	// Deliberately without Ctx. A parked run's context holds the business
	// payload -- amounts, names, hostnames -- and an operator listing needs to
	// show what is waiting and why, not to hand out the contents.
	out := make([]pendingEntry, 0, len(states))
	for _, s := range states {
		out = append(out, pendingEntry{
			Pointer: s.Pointer, Workflow: s.WorkflowID,
			Step: s.StepID, Reason: s.Reason, Audience: s.Audience,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// identify resolves the caller, or writes the rejection and reports false.
func (a *api) identify(w http.ResponseWriter, r *http.Request) (string, bool) {
	if a.cfg.Identify == nil {
		return "", true // AllowAnonymous, checked in New
	}
	identity, err := a.cfg.Identify(r)
	if err != nil {
		// The reason is not echoed: an authentication failure should not
		// describe why to whoever failed it.
		writeError(w, http.StatusUnauthorized, "authentication failed")
		return "", false
	}
	return identity, true
}

func (a *api) decode(w http.ResponseWriter, r *http.Request, into any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, a.cfg.MaxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields() // a misspelled field is a mistake, not a default
	if err := dec.Decode(into); err != nil {
		writeError(w, http.StatusBadRequest, "malformed request body")
		return false
	}
	return true
}

// writeRun maps an engine outcome onto a response.
func (a *api) writeRun(w http.ResponseWriter, result entities.WorkflowResult, err error) {
	switch {
	case err == nil && result.StatePointer != "":
		writeJSON(w, http.StatusOK, runResponse{
			Status: "suspended", Output: result.Output,
			Trace: result.Trace, StatePointer: result.StatePointer,
		})

	case err == nil:
		writeJSON(w, http.StatusOK, runResponse{
			Status: "completed", Output: result.Output, Trace: result.Trace,
		})

	default:
		// A workflow the engine carried to a conclusion is a served request,
		// whatever the conclusion. Only the reason distinguishes them, and it
		// comes from the engine's own error text rather than being invented.
		var tripped *execution.CircuitBreakerTripped
		if errors.As(err, &tripped) {
			writeJSON(w, http.StatusOK, runResponse{
				Status: "failed", Output: result.Output,
				Trace: result.Trace, Reason: tripped.Error(),
			})
			return
		}
		// Anything else is the caller naming something that does not exist, or
		// the deployment being wrong. Report it without a stack of internals.
		writeError(w, http.StatusUnprocessableEntity, err.Error())
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

// MemoryPending adapts execution.MemoryStore, whose Pending returns no error.
type MemoryPending struct{ Store *execution.MemoryStore }

func (m MemoryPending) Pending() ([]execution.State, error) { return m.Store.Pending(), nil }
