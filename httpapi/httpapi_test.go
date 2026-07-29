package httpapi_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/p0nymc1/cee/execution"
	"github.com/p0nymc1/cee/httpapi"
)

// A payments desk: small amounts settle, large ones wait for a finance
// manager, and only a finance manager may release them.
func desk(t *testing.T) (*execution.Engine, *execution.MemoryStore) {
	t.Helper()
	store := execution.NewMemoryStore()
	engine := execution.NewEngine(nil)
	engine.SetStore(store)
	engine.SetAuthorizer(execution.AuthorizerFunc(func(a execution.ResumeAttempt) (bool, string, error) {
		return a.Identity == "wei", "not a finance manager", nil
	}))
	engine.RegisterWorkflow(&execution.Workflow{
		WorkflowID:  "payments.release",
		EntryStepID: "check",
		Steps: map[string]execution.Step{
			"check": &execution.LeafStep{
				StepID: "check", OnSuccess: "pay",
				Run: func(ctx map[string]any) (map[string]any, error) {
					amount, _ := ctx["amount"].(float64)
					if amount > 1000 {
						return execution.SuspendFor("over the limit", "finance-manager")
					}
					return map[string]any{}, nil
				},
			},
			"pay": &execution.LeafStep{StepID: "pay",
				Run: func(map[string]any) (map[string]any, error) {
					return map[string]any{"paid": true}, nil
				}},
		},
	})
	return engine, store
}

func handler(t *testing.T, engine *execution.Engine, store *execution.MemoryStore, identify func(*http.Request) (string, error)) http.Handler {
	t.Helper()
	h, err := httpapi.New(httpapi.Config{
		Engine:   engine,
		Identify: identify,
		Pending:  httpapi.MemoryPending{Store: store},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return h
}

// headerIdentity stands in for real authentication. A production Identify must
// verify something; this one is a test double and says so.
func headerIdentity(r *http.Request) (string, error) {
	who := r.Header.Get("X-Test-Identity")
	if who == "" {
		return "", errors.New("no identity")
	}
	return who, nil
}

func post(h http.Handler, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("response was not JSON: %q", rec.Body.String())
	}
	return out
}

// An unconfigured deployment must not be an open one.
func TestNewRefusesToServeUnauthenticatedByAccident(t *testing.T) {
	engine, _ := desk(t)
	_, err := httpapi.New(httpapi.Config{Engine: engine})
	if err == nil {
		t.Fatal("a config with neither Identify nor AllowAnonymous must be refused")
	}
	if !strings.Contains(err.Error(), "AllowAnonymous") {
		t.Fatalf("the error should name the deliberate opt-out, got %v", err)
	}
	// Opting out on purpose is allowed.
	if _, err := httpapi.New(httpapi.Config{Engine: engine, AllowAnonymous: true}); err != nil {
		t.Fatalf("an explicit opt-out should be accepted: %v", err)
	}
}

func TestRunCompletes(t *testing.T) {
	engine, store := desk(t)
	h := handler(t, engine, store, headerIdentity)

	rec := post(h, "/v1/run", `{"workflow":"payments.release","context":{"amount":20}}`,
		map[string]string{"X-Test-Identity": "wei"})

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body)
	}
	body := decode(t, rec)
	if body["status"] != "completed" {
		t.Fatalf("expected completed, got %v", body["status"])
	}
}

func TestRunSuspendsAndReturnsAPointer(t *testing.T) {
	engine, store := desk(t)
	h := handler(t, engine, store, headerIdentity)

	rec := post(h, "/v1/run", `{"workflow":"payments.release","context":{"amount":5000}}`,
		map[string]string{"X-Test-Identity": "wei"})

	body := decode(t, rec)
	if body["status"] != "suspended" {
		t.Fatalf("expected suspended, got %v", body["status"])
	}
	if body["state_pointer"] == "" || body["state_pointer"] == nil {
		t.Fatal("a suspended run must return its pointer")
	}
}

// The whole reason the authorization work came first.
func TestResumeEnforcesTheAudience(t *testing.T) {
	engine, store := desk(t)
	h := handler(t, engine, store, headerIdentity)

	suspended := decode(t, post(h, "/v1/run", `{"workflow":"payments.release","context":{"amount":5000}}`,
		map[string]string{"X-Test-Identity": "wei"}))
	pointer := suspended["state_pointer"].(string)

	// Someone who holds the pointer but may not approve.
	rec := post(h, "/v1/resume", fmt.Sprintf(`{"pointer":%q,"resolution":{}}`, pointer),
		map[string]string{"X-Test-Identity": "mallory"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for an unauthorised caller, got %d: %s", rec.Code, rec.Body)
	}

	// The approval is still waiting for someone who may give it.
	rec = post(h, "/v1/resume", fmt.Sprintf(`{"pointer":%q,"resolution":{}}`, pointer),
		map[string]string{"X-Test-Identity": "wei"})
	if rec.Code != http.StatusOK {
		t.Fatalf("the authorised caller should succeed, got %d: %s", rec.Code, rec.Body)
	}
	if decode(t, rec)["status"] != "completed" {
		t.Fatalf("expected the run to finish, got %s", rec.Body)
	}
}

// A pointer in a path ends up in access logs, proxy logs and Referer headers,
// and it authorises an approval. It travels in the body instead.
func TestThePointerIsNotAPathParameter(t *testing.T) {
	engine, store := desk(t)
	h := handler(t, engine, store, headerIdentity)

	suspended := decode(t, post(h, "/v1/run", `{"workflow":"payments.release","context":{"amount":5000}}`,
		map[string]string{"X-Test-Identity": "wei"}))
	pointer := suspended["state_pointer"].(string)

	// There is no route that would put it in the URL.
	req := httptest.NewRequest(http.MethodPost, "/v1/resume/"+pointer, strings.NewReader("{}"))
	req.Header.Set("X-Test-Identity", "wei")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		t.Fatal("a pointer must not be accepted from the URL path")
	}
}

func TestUnauthenticatedRequestsAreRejected(t *testing.T) {
	engine, store := desk(t)
	h := handler(t, engine, store, headerIdentity)

	for _, path := range []string{"/v1/run", "/v1/resume"} {
		rec := post(h, path, `{}`, nil) // no identity header
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s should have been rejected, got %d", path, rec.Code)
		}
		// A failed authentication should not explain itself to whoever failed it.
		if strings.Contains(rec.Body.String(), "no identity") {
			t.Fatalf("the rejection leaked the internal reason: %s", rec.Body)
		}
	}
}

// A run the engine carried to a conclusion is a served request, whatever the
// conclusion. 500 would claim the service is broken when it is not.
func TestAnAbandonedRunIsAServedRequestNotAServerError(t *testing.T) {
	store := execution.NewMemoryStore()
	engine := execution.NewEngine(nil)
	engine.SetStore(store)
	engine.RegisterWorkflow(&execution.Workflow{
		WorkflowID: "boom", EntryStepID: "fail",
		Steps: map[string]execution.Step{
			"fail": &execution.LeafStep{StepID: "fail",
				Run: func(map[string]any) (map[string]any, error) {
					return nil, errors.New("upstream is down")
				}},
		},
	})
	h := handler(t, engine, store, headerIdentity)

	rec := post(h, "/v1/run", `{"workflow":"boom","context":{}}`,
		map[string]string{"X-Test-Identity": "wei"})

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for a workflow that ran and failed, got %d", rec.Code)
	}
	body := decode(t, rec)
	if body["status"] != "failed" {
		t.Fatalf("expected failed, got %v", body["status"])
	}
	if !strings.Contains(fmt.Sprint(body["reason"]), "upstream is down") {
		t.Fatalf("the reason should reach the caller, got %v", body["reason"])
	}
}

// The operator listing shows what is waiting and why -- not the payload. A
// parked run's context holds amounts, names and hostnames.
func TestPendingListingOmitsTheBusinessPayload(t *testing.T) {
	engine, store := desk(t)
	h := handler(t, engine, store, headerIdentity)

	post(h, "/v1/run", `{"workflow":"payments.release","context":{"amount":5000,"claimant":"wei"}}`,
		map[string]string{"X-Test-Identity": "wei"})

	req := httptest.NewRequest(http.MethodGet, "/v1/pending", nil)
	req.Header.Set("X-Test-Identity", "wei")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "over the limit") || !strings.Contains(body, "finance-manager") {
		t.Fatalf("the listing should say what is waiting and for whom: %s", body)
	}
	if strings.Contains(body, "claimant") || strings.Contains(body, "5000") {
		t.Fatalf("the listing must not hand out the parked context: %s", body)
	}
}

func TestPendingRequiresAuthenticationToo(t *testing.T) {
	engine, store := desk(t)
	h := handler(t, engine, store, headerIdentity)

	req := httptest.NewRequest(http.MethodGet, "/v1/pending", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("the operator listing must not be open, got %d", rec.Code)
	}
}

func TestGetCannotStartARun(t *testing.T) {
	engine, store := desk(t)
	h := handler(t, engine, store, headerIdentity)

	req := httptest.NewRequest(http.MethodGet, "/v1/run", nil)
	req.Header.Set("X-Test-Identity", "wei")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		t.Fatal("a GET must not execute a workflow")
	}
}

func TestAnOversizedBodyIsRefused(t *testing.T) {
	engine, _ := desk(t)
	h, err := httpapi.New(httpapi.Config{
		Engine: engine, Identify: headerIdentity, MaxBodyBytes: 64,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	huge := fmt.Sprintf(`{"workflow":"payments.release","context":{"pad":%q}}`, strings.Repeat("x", 500))
	rec := post(h, "/v1/run", huge, map[string]string{"X-Test-Identity": "wei"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("an oversized body should be refused, got %d", rec.Code)
	}
}

// A misspelled field is a mistake, not a silent default.
func TestUnknownFieldsAreRejected(t *testing.T) {
	engine, store := desk(t)
	h := handler(t, engine, store, headerIdentity)

	rec := post(h, "/v1/run", `{"workflow":"payments.release","contxt":{"amount":20}}`,
		map[string]string{"X-Test-Identity": "wei"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("a misspelled field should be refused rather than ignored, got %d", rec.Code)
	}
}

// With AllowAnonymous the engine still refuses audienced suspensions, which is
// the point: development convenience does not become an approval bypass.
func TestAnonymousModeStillCannotAnswerAnAudiencedSuspension(t *testing.T) {
	engine, store := desk(t)
	h, err := httpapi.New(httpapi.Config{
		Engine: engine, AllowAnonymous: true, Pending: httpapi.MemoryPending{Store: store},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	suspended := decode(t, post(h, "/v1/run", `{"workflow":"payments.release","context":{"amount":5000}}`, nil))
	pointer := suspended["state_pointer"].(string)

	rec := post(h, "/v1/resume", fmt.Sprintf(`{"pointer":%q,"resolution":{}}`, pointer), nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("anonymous must not satisfy an audience, got %d: %s", rec.Code, rec.Body)
	}
}
