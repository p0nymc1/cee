package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/p0nymc1/cee/entities"
	"github.com/p0nymc1/cee/execution"
)

const DefaultMaxBodyBytes = 1 << 20

type PendingLister interface {
	Pending() ([]execution.State, error)
}

type Config struct {
	Engine *execution.Engine

	Identify func(*http.Request) (string, error)

	AllowAnonymous bool

	Pending PendingLister

	MaxBodyBytes int64
}

type api struct {
	cfg Config
}

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

type runRequest struct {
	Workflow string         `json:"workflow"`
	Context  map[string]any `json:"context"`
}

type resumeRequest struct {
	Pointer    string         `json:"pointer"`
	Resolution map[string]any `json:"resolution"`
}

type runResponse struct {
	Status       string         `json:"status"`
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

	out := make([]pendingEntry, 0, len(states))
	for _, s := range states {
		out = append(out, pendingEntry{
			Pointer: s.Pointer, Workflow: s.WorkflowID,
			Step: s.StepID, Reason: s.Reason, Audience: s.Audience,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *api) identify(w http.ResponseWriter, r *http.Request) (string, bool) {
	if a.cfg.Identify == nil {
		return "", true
	}
	identity, err := a.cfg.Identify(r)
	if err != nil {

		writeError(w, http.StatusUnauthorized, "authentication failed")
		return "", false
	}
	return identity, true
}

func (a *api) decode(w http.ResponseWriter, r *http.Request, into any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, a.cfg.MaxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		writeError(w, http.StatusBadRequest, "malformed request body")
		return false
	}
	return true
}

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

		var tripped *execution.CircuitBreakerTripped
		if errors.As(err, &tripped) {
			writeJSON(w, http.StatusOK, runResponse{
				Status: "failed", Output: result.Output,
				Trace: result.Trace, Reason: tripped.Error(),
			})
			return
		}

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

type MemoryPending struct{ Store *execution.MemoryStore }

func (m MemoryPending) Pending() ([]execution.State, error) { return m.Store.Pending(), nil }
