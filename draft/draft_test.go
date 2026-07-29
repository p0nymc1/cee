package draft_test

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/p0nymc1/cee/draft"
	"github.com/p0nymc1/cee/llmhttp"
	"github.com/p0nymc1/cee/manifest"
	"github.com/p0nymc1/cee/stdlib"
)

// scriptedModel replies with the next canned answer each call, so the tests
// exercise the loop without touching a network or a model.
type scriptedModel struct {
	replies []string
	calls   int
}

func (m *scriptedModel) Do(*http.Request) (*http.Response, error) {
	reply := "{}"
	if m.calls < len(m.replies) {
		reply = m.replies[m.calls]
	}
	m.calls++

	envelope, _ := json.Marshal(map[string]any{
		"choices": []any{map[string]any{"message": map[string]any{"content": reply}}},
	})
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(string(envelope))),
	}, nil
}

func configWith(model *scriptedModel, maxAttempts int) draft.Config {
	return draft.Config{
		LLM:         llmhttp.Config{BaseURL: "http://example.invalid", Model: "test", HTTPClient: model},
		MaxAttempts: maxAttempts,
	}
}

const goodManifest = `{
  "name": "expenses",
  "intents": [{"node_id":"expenses.screen","examples":["screen this expense"],"entry_workflow_ref":"expenses.screen"}],
  "policies": [{"policy_id":"route_to_flag","fallback_step_ref":"flag"}],
  "workflows": [{
    "workflow_id":"expenses.screen","entry_step_id":"check",
    "steps":[
      {"step_id":"check","type":"leaf","action_ref":"std.require",
       "with":{"field":"amount","op":"lte","value":10000},
       "circuit_breaker_policy_ref":"route_to_flag","on_success":"approve"},
      {"step_id":"approve","type":"leaf","action_ref":"std.set","with":{"fields":{"approved":true}}},
      {"step_id":"flag","type":"leaf","action_ref":"std.set","with":{"fields":{"flagged":true}}}
    ]}]
}`

func TestAValidDraftIsReturnedAndLoads(t *testing.T) {
	model := &scriptedModel{replies: []string{goodManifest}}

	result, err := draft.Draft(configWith(model, 3), "approve small expenses, flag large ones", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if model.calls != 1 {
		t.Fatalf("a valid first draft should not be retried, called %d times", model.calls)
	}
	// The contract: what comes back is not merely parseable, it binds.
	if _, err := manifest.Load(result.Manifest, nil, stdlib.Default()); err != nil {
		t.Fatalf("a returned draft must load: %v", err)
	}
}

// The correction loop: a first attempt with a dangling reference is fed its
// own validation errors and gets it right on the second pass.
func TestAnInvalidDraftIsCorrectedFromItsErrors(t *testing.T) {
	broken := `{
	  "name":"expenses",
	  "workflows":[{"workflow_id":"expenses.screen","entry_step_id":"check",
	    "steps":[{"step_id":"check","type":"leaf","action_ref":"std.set",
	              "with":{"fields":{}},"on_success":"nowhere"}]}]}`

	model := &scriptedModel{replies: []string{broken, goodManifest}}
	result, err := draft.Draft(configWith(model, 3), "approve small expenses", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Attempts) != 2 {
		t.Fatalf("expected two attempts, got %d", len(result.Attempts))
	}
	if result.Attempts[0].Report.OK() {
		t.Fatal("the first attempt should have been rejected")
	}
}

// The property this whole project exists to protect. An unbounded
// correct-and-retry loop would be the very failure mode CEE was built to
// avoid, so a draft that never validates stops and says so.
func TestTheLoopIsBounded(t *testing.T) {
	alwaysBroken := `{"name":"x","workflows":[{"workflow_id":"x.wf","entry_step_id":"missing","steps":[]}]}`
	model := &scriptedModel{replies: []string{alwaysBroken, alwaysBroken, alwaysBroken, alwaysBroken, alwaysBroken}}

	_, err := draft.Draft(configWith(model, 3), "do something", nil)
	if err == nil {
		t.Fatal("a draft that never validates must fail, not loop")
	}
	if model.calls != 3 {
		t.Fatalf("MaxAttempts must be a hard ceiling, model called %d times", model.calls)
	}
}

// A model that invents an action must not get a manifest past the gate. This
// is what makes it safe to let a model author something the engine executes:
// the worst it can produce is a wrong arrangement of actions that already
// existed.
func TestAnInventedActionIsNeverAccepted(t *testing.T) {
	invented := `{
	  "name":"evil",
	  "intents":[{"node_id":"evil.go","examples":["x"],"entry_workflow_ref":"evil.wf"}],
	  "workflows":[{"workflow_id":"evil.wf","entry_step_id":"a",
	    "steps":[{"step_id":"a","type":"leaf","action_ref":"os.exec.rm_rf"}]}]}`

	model := &scriptedModel{replies: []string{invented, invented, invented}}
	result, err := draft.Draft(configWith(model, 3), "delete everything", nil)
	if err == nil {
		t.Fatal("a manifest naming an action that does not exist must be refused")
	}
	if result.Manifest != nil {
		t.Fatal("no manifest should be returned when none validated")
	}
}

// Validate only warns about an unknown action, because it cannot see a
// domain's Go hooks. When the draft claims to need none, Load is what proves
// the claim -- so a warning-only manifest still must not slip through.
func TestAWarningOnlyDraftIsStillBoundBeforeAcceptance(t *testing.T) {
	// Passes Validate with a warning (unknown action), fails Load with no hooks.
	hookish := `{
	  "name":"d",
	  "intents":[{"node_id":"d.go","examples":["x"],"entry_workflow_ref":"d.wf"}],
	  "workflows":[{"workflow_id":"d.wf","entry_step_id":"a",
	    "steps":[{"step_id":"a","type":"leaf","action_ref":"d.custom_hook"}]}]}`

	if !manifest.Validate([]byte(hookish), stdlib.Default()).OK() {
		t.Fatal("precondition: this manifest should pass validation with only a warning")
	}

	model := &scriptedModel{replies: []string{hookish, goodManifest}}
	result, err := draft.Draft(configWith(model, 3), "do a thing", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if model.calls != 2 {
		t.Fatalf("the unbindable draft should have been sent back, calls=%d", model.calls)
	}
	if _, err := manifest.Load(result.Manifest, nil, stdlib.Default()); err != nil {
		t.Fatalf("the accepted draft must load: %v", err)
	}
}

// When the domain does declare hooks, an action naming one of them is
// legitimate and must not be bounced.
func TestDeclaredHooksAreAccepted(t *testing.T) {
	withHook := `{
	  "name":"d",
	  "intents":[{"node_id":"d.go","examples":["x"],"entry_workflow_ref":"d.wf"}],
	  "workflows":[{"workflow_id":"d.wf","entry_step_id":"a",
	    "steps":[{"step_id":"a","type":"leaf","action_ref":"d.custom_hook"}]}]}`

	model := &scriptedModel{replies: []string{withHook}}
	cfg := configWith(model, 3)
	cfg.HookNames = []string{"d.custom_hook"}

	if _, err := draft.Draft(cfg, "do a thing", nil); err != nil {
		t.Fatalf("a draft using a declared hook should be accepted: %v", err)
	}
	if model.calls != 1 {
		t.Fatalf("it should not have been retried, calls=%d", model.calls)
	}
}

func TestAnEmptyDescriptionIsRefusedWithoutCallingTheModel(t *testing.T) {
	model := &scriptedModel{replies: []string{goodManifest}}
	if _, err := draft.Draft(configWith(model, 3), "   ", nil); err == nil {
		t.Fatal("an empty description should be refused")
	}
	if model.calls != 0 {
		t.Fatal("nothing should have been asked of the model")
	}
}

// The prompt is the only thing standing between a model and an invalid
// manifest, and the vocabulary it lists is written by hand. If stdlib gains an
// action nobody described, drafts silently stop being able to use it.
func TestVocabularyCoversEveryStandardAction(t *testing.T) {
	model := &scriptedModel{replies: []string{goodManifest}}
	cfg := configWith(model, 1)

	// The prompt is not exported; a successful draft proves it was built, and
	// the check below reads it back out of the request the model received.
	captured := &capturingModel{}
	cfg.LLM.HTTPClient = captured
	_, _ = draft.Draft(cfg, "anything", nil)

	for name := range stdlib.Default() {
		if !strings.Contains(captured.system, name) {
			t.Fatalf("standard action %q is not described in the draft prompt; "+
				"a model cannot use what it is never told about", name)
		}
	}
}

type capturingModel struct{ system string }

func (c *capturingModel) Do(req *http.Request) (*http.Response, error) {
	body, _ := io.ReadAll(req.Body)
	var parsed struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	_ = json.Unmarshal(body, &parsed)
	for _, m := range parsed.Messages {
		if m.Role == "system" {
			c.system = m.Content
		}
	}
	envelope, _ := json.Marshal(map[string]any{
		"choices": []any{map[string]any{"message": map[string]any{"content": "{}"}}},
	})
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(envelope)))}, nil
}

// Branching through the circuit breaker is the one rule no model will infer,
// so the prompt has to say it outright.
func TestPromptExplainsTheBranchingIdiom(t *testing.T) {
	captured := &capturingModel{}
	cfg := configWith(&scriptedModel{}, 1)
	cfg.LLM.HTTPClient = captured
	_, _ = draft.Draft(cfg, "anything", nil)

	for _, phrase := range []string{"no if/else", "circuit_breaker_policy_ref", "No cycles"} {
		if !strings.Contains(captured.system, phrase) {
			t.Fatalf("the prompt must state %q, or drafts will keep getting it wrong", phrase)
		}
	}
}
