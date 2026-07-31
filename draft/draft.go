package draft

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/p0nymc1/cee/llmhttp"
	"github.com/p0nymc1/cee/manifest"
	"github.com/p0nymc1/cee/stdlib"
)

const DefaultMaxAttempts = 3

type Config struct {
	LLM llmhttp.Config

	MaxAttempts int

	HookNames []string
}

type Attempt struct {
	Manifest []byte
	Report   manifest.Report
	Err      error
}

type Result struct {
	Manifest []byte
	Attempts []Attempt
}

func Draft(cfg Config, description string, std stdlib.Library) (Result, error) {
	if strings.TrimSpace(description) == "" {
		return Result{}, fmt.Errorf("draft: a description is required")
	}
	attempts := cfg.MaxAttempts
	if attempts <= 0 {
		attempts = DefaultMaxAttempts
	}
	if std == nil {
		std = stdlib.Default()
	}

	system := systemPrompt(std, cfg.HookNames)
	user := description
	var result Result

	for i := 0; i < attempts; i++ {
		reply, err := llmhttp.Chat(cfg.LLM, system, user)
		if err != nil {
			result.Attempts = append(result.Attempts, Attempt{Err: err})
			return result, fmt.Errorf("draft: attempt %d: %w", i+1, err)
		}

		candidate := []byte(reply)
		report := manifest.Validate(candidate, std)
		attempt := Attempt{Manifest: candidate, Report: report}

		if report.OK() {

			if len(cfg.HookNames) == 0 {
				if _, err := manifest.Load(candidate, nil, std); err != nil {
					attempt.Err = err
					result.Attempts = append(result.Attempts, attempt)
					user = retryPrompt(description, err.Error())
					continue
				}
			}
			result.Attempts = append(result.Attempts, attempt)
			result.Manifest = candidate
			return result, nil
		}

		result.Attempts = append(result.Attempts, attempt)
		user = retryPrompt(description, report.String())
	}

	return result, fmt.Errorf("draft: no valid manifest after %d attempts; last issues:\n%s",
		attempts, lastIssues(result))
}

func lastIssues(r Result) string {
	if len(r.Attempts) == 0 {
		return "(none recorded)"
	}
	last := r.Attempts[len(r.Attempts)-1]
	if last.Err != nil {
		return last.Err.Error()
	}
	return last.Report.String()
}

func retryPrompt(description, problems string) string {
	return fmt.Sprintf(
		"Your previous manifest was rejected. Fix exactly these problems and return the "+
			"corrected manifest as raw JSON:\n\n%s\n\nThe original request was:\n%s",
		problems, description)
}

var vocabulary = map[string]string{
	"std.set": `{"action_ref":"std.set","with":{"fields":{"approved":true}}} — ` +
		`writes fixed fields into the output. Use it for terminal steps that record an outcome.`,
	"std.require": `{"action_ref":"std.require","with":{"field":"amount","op":"lte","value":10000}} — ` +
		`asserts a condition. If it holds the step succeeds and on_success is taken; if it does not ` +
		`the step FAILS, and its circuit_breaker_policy_ref routes to that policy's fallback step. ` +
		`This is how branching is expressed: there is no if/else.`,
	"std.rule_check": `{"action_ref":"std.rule_check","with":{"field":"amount","op":"gt","value":10000,"result_field":"is_large"}} — ` +
		`computes a boolean into result_field without affecting control flow.`,
	"std.suspend": `{"action_ref":"std.suspend","with":{"reason":"awaiting manager approval"}} — ` +
		`pauses the run until something outside resumes it. Not a failure. Use for human approval or a time window.`,
	"std.require_verified": `{"action_ref":"std.require_verified","with":{"fields":["amount"]}} — ` +
		`refuses to proceed when the named fields came from a model rather than a system of record. ` +
		`Put it in front of consequential steps.`,
}

func systemPrompt(std stdlib.Library, hooks []string) string {
	var b strings.Builder
	b.WriteString(`You design workflows for a deterministic execution engine. Return ONLY a JSON manifest, no prose.

Shape:
{
  "name": "<domain-name>",
  "intents": [{"node_id":"<domain>.<snake_case>","examples":["natural phrasings"],"entry_workflow_ref":"<workflow_id>"}],
  "policies": [{"policy_id":"<snake_case>","fallback_step_ref":"<step_id>"}],
  "workflows": [{
    "workflow_id":"<domain>.<snake_case>",
    "entry_step_id":"<step_id>",
    "steps":[{"step_id":"<snake_case>","type":"leaf","action_ref":"<action>","with":{},
              "circuit_breaker_policy_ref":"<policy_id>","on_success":"<step_id>",
              "compensate_with":"<step_id>"}]
  }]
}

Rules you cannot infer and must follow:
- There is no if/else. A step has two exits: on_success when it succeeds, and its
  circuit_breaker_policy_ref's fallback when it fails. Express a decision with std.require
  plus a policy whose fallback is the "otherwise" branch.
- Chain std.require steps to express more than two outcomes; the order encodes precedence.
- Every step named by on_success, fallback_step_ref or compensate_with must exist in the
  same workflow. Every entry_workflow_ref must match a workflow_id.
- No cycles. on_success must never lead back to an earlier step.
- compensate_with names a step that undoes this one, used when a run is abandoned. Declare it
  on any step with an irreversible effect.
- Use only the actions listed below. Do not invent action names.

Available actions:
`)

	names := make([]string, 0, len(std))
	for name := range std {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if desc, ok := vocabulary[name]; ok {
			fmt.Fprintf(&b, "- %s\n", desc)
		} else {
			fmt.Fprintf(&b, "- %s\n", name)
		}
	}

	if len(hooks) > 0 {
		b.WriteString("\nDomain actions (already implemented, reference by name; they take no \"with\" block):\n")
		sorted := append([]string(nil), hooks...)
		sort.Strings(sorted)
		for _, h := range sorted {
			fmt.Fprintf(&b, "- %s\n", h)
		}
	} else {
		b.WriteString("\nNo domain actions are available. Use only the standard actions above.\n")
	}

	b.WriteString("\nComparison operators: eq, neq, gt, gte, lt, lte, in.\n")
	return b.String()
}

func Pretty(m []byte) []byte {
	var v any
	if err := json.Unmarshal(m, &v); err != nil {
		return m
	}
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return m
	}
	return append(out, '\n')
}
