# CEE Development Guide

For people who are going to develop *on* this codebase — whether modifying the engine itself or plugging in a new
domain plugin. Architecture rationale is in [`TECHNICAL_SPECIFICATION.md`](TECHNICAL_SPECIFICATION.md);
contribution rules are in [`NORMATIVE_HANDBOOK.md`](NORMATIVE_HANDBOOK.md). This document only covers how to get
your hands on it.

## 1. Requirements

- **Go 1.26 or later** — `go.mod` declares `go 1.26.5`, and since Go 1.21 that is a hard floor. Older toolchains
  refuse to build (or auto-download 1.26 per `GOTOOLCHAIN`, which needs network access).
- No external dependencies — `go.mod` has no `require` entries, so `go build`/`go test` work offline (provided the
  local Go is already ≥ 1.26).

```bash
go version        # confirm >= 1.26
go build ./...     # compile every package
go vet ./...        # static checks
go test ./... -v    # run every test
make stats          # print the repo figures the docs quote
```

## 2. Project layout

```
cee/
  go.mod
  entities/       shared data contracts; everything depends on it, it depends on nothing
  intentrouter/   intent routing
  execution/      deterministic execution engine (DAG walking, circuit breakers)
  llminjector/    edge LLM extractor
  sandbox/        pre-execution sandbox
  registry/       domain registry (plugs a plugin into Router + Engine)
  manifest/       declarative JSON loader (Load produces a registry.Domain) + static validator (Validate)
  stdlib/         standard action library (std.set / std.require / std.rule_check / std.suspend / std.require_verified)
  filestore/      durable execution.Store; suspended state survives restarts
  llmhttp/        real LLM backend: net/http against an OpenAI-compatible endpoint, produces an llminjector.Extractor
  embedhttp/      real semantic backend: net/http against an embedding endpoint, produces an intentrouter.Vectorizer
  replay/         record/replay the non-deterministic entry points (probes + extraction) for rule-change regression diffs
  draft/          have a model draft a manifest, behind four validation gates
  httpapi/        a mountable http.Handler; anonymous callers denied by default
  scorecard/      per-request metrics: deterministic steps / LLM calls / sandbox / breakers / elapsed
  diagnostics/    cross-run error metrics: intent miss rate / probe refusal rate / escalation rate
  catalog/        community distribution: index.json + plugins/<name>/manifest.json (+ benchmark.json)
  bench/          benchmark batches: run standard events through plugins, aggregate Scorecards, rank
  cmd/cee/        CLI: validate / lint / list / install / bench / draft / serve
  docs/           this document's directory
  examples/       nine runnable examples, all compiled and tested with the repo
    quickstart/            minimal integration: a refund desk (pay / park for a manager / blocked by a probe)
    rule_change/           change one rule, replay past decisions, compute which flip
    code_audit/            AI PR-review agent re-cast: extract-only model + verified severity + blast-radius gate
    security_monitoring/   L2 example: Go plugin + sandbox gating + breaker escalation to human review
    network_detection/     ATT&CK matching + blast-radius guardrails on containment
    crypto_surveillance/   live market anomaly monitoring (uses the network)
    human_approval/        L1, zero Go: suspend / resume
    meta_scenarios/        ticket routing / scheduling / data sync
    local_netwatch/        local connection screening
    manifests/             L1 examples: expense-guard.json and others, pure JSON, zero Go
  satellites/     satellite modules with their own go.mod: dockersandbox / httpsandbox / wasmhooks
```

Dependencies between packages are one-directional; there are no import cycles:

```
entities  ←  intentrouter, execution, llminjector, sandbox
execution ←  registry, manifest, stdlib
intentrouter ← registry, manifest
registry  ←  manifest
stdlib    ←  manifest
manifest, stdlib  ←  cmd/cee
```

`scorecard` is a leaf package: it imports no other cee package, satisfying both `execution.Observer` and
`llminjector.Observer` structurally through its method set. So instrumentation creates no reverse
`scorecard → execution` dependency.

## 3. Quick start: getting one domain running from scratch

There are two equivalent ways to define a domain plugin. Both produce the same `registry.Domain`, and they can be
mixed.

### Option 1: hand-written Go structs (for scenarios needing complex Go logic)

```go
package main

import (
    "github.com/p0nymc1/cee/entities"
    "github.com/p0nymc1/cee/execution"
    "github.com/p0nymc1/cee/intentrouter"
    "github.com/p0nymc1/cee/registry"
)

func main() {
    router := intentrouter.NewRouter(0.5) // tune the threshold per scenario
    engine := execution.NewEngine(nil)     // nil when no sandbox is needed
    reg := registry.NewRegistry(router, engine)

    reg.RegisterDomain(registry.Domain{
        Name: "finance",
        Intents: []entities.IntentNode{{
            NodeID:       "finance.duplicate_expense",
            DomainID:     "finance",
            Examples:     []string{"duplicate expense report"},
            EntryWorkflowRef: "finance.flag_duplicate", // points at a Workflow's WorkflowID below
        }},
        Workflows: []*execution.Workflow{{
            WorkflowID:  "finance.flag_duplicate",
            EntryStepID: "check",
            Steps: map[string]execution.Step{
                "check": &execution.LeafStep{
                    StepID: "check",
                    Run: func(ctx map[string]any) (map[string]any, error) {
                        return map[string]any{"flagged": true}, nil
                    },
                },
            },
        }},
    })

    match := router.Match("finance", "duplicate expense report submitted again")
    if match.Matched {
        result, _ := engine.Run(match.EntryWorkflowRef, map[string]any{})
        _ = result // result.Output["flagged"] == true
    }
}
```

### Option 2: JSON manifest + named hooks (for handing the DAG's shape to non-Go developers)

1. Write a manifest file (see `financeManifestJSON` in `manifest/manifest_test.go` for the structure):

```json
{
  "name": "finance",
  "intents": [
    {"node_id": "finance.duplicate_expense", "examples": ["duplicate expense report"], "entry_workflow_ref": "finance.flag_duplicate"}
  ],
  "policies": [
    {"policy_id": "escalate_to_review", "fallback_step_ref": "human_review"}
  ],
  "workflows": [{
    "workflow_id": "finance.flag_duplicate",
    "entry_step_id": "check",
    "steps": [
      {"step_id": "check", "type": "leaf", "action_ref": "finance.check_duplicate",
       "circuit_breaker_policy_ref": "escalate_to_review", "on_success": "notify"},
      {"step_id": "notify", "type": "leaf", "action_ref": "finance.notify_finance_team"},
      {"step_id": "human_review", "type": "leaf", "action_ref": "finance.queue_human_review"}
    ]
  }]
}
```

2. In Go, write only the named functions — never the DAG structure:

```go
hooks := manifest.Hooks{
    "finance.check_duplicate":     checkDuplicateAction,
    "finance.notify_finance_team": notifyAction,
    "finance.queue_human_review":  queueHumanReviewAction,
}

// The third argument is the standard action library. An action_ref in a manifest is looked
// up in the standard library first, then in hooks. Both may be nil.
domain, err := manifest.Load(manifestJSONBytes, hooks, stdlib.Default())
if err != nil {
    // The manifest referenced an action_ref present in neither the standard library nor hooks,
    // or the JSON is malformed, or a composite step is missing sub_workflow_ref. Load errors
    // explicitly rather than silently producing half a Domain.
}
reg.RegisterDomain(*domain)
```

### Option 2 addendum: purely declarative (zero Go)

If a process only uses generic actions from the standard library (`std.set`/`std.require`/`std.rule_check`/
`std.suspend`/`std.require_verified`), pass `nil` for `hooks` and the plugin author writes no Go at all — this is the
community L1 tier. Standard actions take parameters via each step's `with` block:

```json
{"step_id": "check_threshold", "type": "leaf", "action_ref": "std.require",
 "with": {"field": "amount", "op": "lte", "value": 10000},
 "circuit_breaker_policy_ref": "route_to_flag", "on_success": "approve"}
```

`std.require` is the idiom for branching in an engine with no if/else: the condition holding takes `on_success`, and it
not holding **fails**, routing through `circuit_breaker_policy_ref` to a fallback step. Complete runnable example:
`examples/manifests/expense-guard.json`.

### Parallel branches (`type: "parallel"`)

When several independent checks should run at once and be decided together, use a `parallel` step whose branches are
sub-workflows:

```json
{"step_id": "run_checks", "type": "parallel",
 "branches": ["onboarding.credit_check", "onboarding.sanctions_check"],
 "circuit_breaker_policy_ref": "route_to_manual_review", "on_success": "decide"}
```

What to know (detail in specification 5.9):

- Branches really do run concurrently, but **the join and the trace always follow declaration order**, so the result
  does not depend on which finishes first.
- Branches cannot see each other's writes; each starts from a copy of the incoming context.
- **Two branches writing the same field with different values is refused** (`*ConflictingBranchWrites`) rather than
  arbitrated by order — have them write different fields.
- A branch cannot suspend (`std.suspend` inside one returns `*NestedSuspensionUnsupported`).

Complete no-code example: `examples/manifests/onboarding-checks.json`.

### Self-check with `cee validate` before submitting

`cmd/cee` provides a static validator that turns structural and reference integrity checks into one command. Purely
declarative manifests can be validated completely (a step referencing a custom Go hook gets structural validation
only; whether the hook exists is checked by `Load` at runtime, and validate flags it as a warning):

```bash
go run ./cmd/cee validate examples/manifests/expense-guard.json
# ok: no issues        -> exit 0
# [error] ...           -> exit 1 (usable directly as a CI gate)
```

It catches: dangling `on_success`/`sub_workflow_ref`/`entry_workflow_ref`, references to undeclared
`circuit_breaker_policy_ref`, breaker fallbacks pointing at non-existent steps, wrong standard action parameters,
duplicate step_ids, and the `on_success` / `sub_workflow_ref` cycles that would make `Engine.Run` run away.

### Publishing a plugin to the catalog (community distribution)

`catalog/` is a git-based plugin directory — an `index.json` plus the manifest files it points at. Contributing an L1
plugin is one PR:

```bash
go run ./cmd/cee list             # list the plugins in the catalog
go run ./cmd/cee lint             # validate the whole catalog (CI gate; exit 1 = problems)
go run ./cmd/cee install sla-guard # validate, then pull the manifest into ./plugins/
```

Publishing steps: put your manifest at `catalog/plugins/<name>/manifest.json`, add an entry to `catalog/index.json`
(`name`/`tier`/`version`/`domain`/`manifest` path), run `cee lint` to confirm it's clean, and open a PR. `install`
**validates before writing to disk** — a plugin that fails `cee validate` never gets installed. L2 plugins needing a
Go hook are distributed as Go modules rather than via the catalog's `install` (but can be registered in the index with
`tier: "L2"` so they are discoverable).

To get on the leaderboard: add a `benchmark` field pointing at `plugins/<name>/benchmark.json` (a set of
`{workflow_ref, context}` standard events), then:

```bash
go run ./cmd/cee bench             # run every plugin with a benchmark, ranked by determinism ratio
```

The ranking basis is "how many calls were eliminated versus an agent making one LLM call per step" — the social
mechanism that gets contributors optimising their processes for a number worth bragging about.

An `action_ref` not found in `hooks`, a `type` that is neither `"leaf"` nor `"composite"`, and a `composite` missing
`sub_workflow_ref` — `Load` returns an `error` with context (domain name / workflow name / step name) for all three,
so you can find which step of which manifest is wrong.

## 4. Adding sandbox gating to a step

Set `SandboxProbeRef` on the `LeafStep`, and register a probe of the same name on the `sandbox` passed to
`execution.NewEngine`:

```go
sb := sandbox.NewSandbox()
sb.RegisterProbe("check_impact", func(ctx map[string]any) (bool, string, error) {
    // read-only / simulated checks only; never any real side effect
    if ctx["target_host"] == "dc01" {
        return false, "would isolate a domain controller", nil
    }
    return true, "", nil
})

engine := execution.NewEngine(sb) // Sandbox satisfies execution.Prober
```

`Engine.Run` calls this probe before running that step's `Run`. When the probe returns `healthy=false`, the step's real
action **does not execute**, and control goes straight to the step's declared `CircuitBreakerPolicyRef`.

## 5. Adding schema validation to an extraction

```go
inj := llminjector.NewInjector()
inj.RegisterSchema("finance.expense_fields",
    llminjector.Schema{"amount": llminjector.FieldFloat64, "category": llminjector.FieldString},
    func(rawText string) (map[string]any, error) {
        // call your real small model / rules here; the return value may contain extra fields —
        // Extract keeps only the ones the schema declared
        return callYourLLM(rawText)
    },
)

result := inj.Extract(entities.ExtractionRequest{
    RawText: "taxi to airport $4200", SchemaRef: "finance.expense_fields", DomainID: "finance",
})
if result.Success {
    amount := result.StructuredPayload["amount"].(float64)
}
```

## 5b. Measuring a request (Scorecard)

`scorecard.Recorder` is per-request: create one per request, attach it to the engine (and injector), and `Snapshot`
when done. The engine carries no observer by default, at zero overhead; callbacks only happen once attached.

```go
recorder := scorecard.NewRecorder()
engine.SetObserver(recorder)          // engine side: steps / sandbox / breakers
injector.SetObserver(recorder)         // injector side: LLM extraction count (only needed when an LLM is involved)

result, _ := engine.Run(entryRef, ctx)

card := recorder.Snapshot(entryRef)
fmt.Println(card)                       // determinism 100% (2 deterministic steps, 0 LLM calls)...
_ = card.DeterminismRatio()             // 0.0–1.0, = the share of calls saved vs a per-step-LLM agent
```

What is counted is **actual execution**: a step blocked by the sandbox whose action never ran does not count as a
deterministic step (it counts as a sandbox rehearsal plus a breaker). Full usage in
`examples/security_monitoring`.

## 5c. Measuring the error side (diagnostics)

Where the scorecard is per-request and reports what went well, `diagnostics.Recorder` is long-lived and reports what
went wrong across many runs: intent miss rate, probe refusal rate, escalation rate. Attach one recorder to both the
router and the engine, and call `ObserveRun` once per request so escalation has a denominator.

```go
diag := diagnostics.NewRecorder()
router.SetObserver(diag)   // intent hit/miss
engine.SetObserver(diag)   // probe outcomes, suspensions, breaker trips

for _, req := range batch {
    diag.ObserveRun()
    match := router.Match(domain, req.Text)
    if match.Matched {
        engine.Run(match.EntryWorkflowRef, req.Ctx)
    }
}

fmt.Println(diag.Report())  // intent miss 20% (1 of 5), probe refusal 25% (1 of 4), ...
```

The engine has one observer slot, so a deployment wanting both per-request scorecards and aggregate diagnostics runs
them on separate passes or composes its own forwarding observer. `examples/security_monitoring` shows the aggregate
batch alongside its per-event scorecards.

## 6. API quick reference

| Package | Constructor | Key methods |
|---|---|---|
| `intentrouter` | `NewRouter(threshold float64) *Router` | `RegisterNode(entities.IntentNode)`, `Match(domainID, rawText string) entities.MatchResult`, `SetVectorizer(Vectorizer)` |
| `execution` | `NewEngine(sandbox Prober) *Engine` | `RegisterWorkflow(*Workflow)`, `RegisterPolicy(CircuitBreakerPolicy)`, `SetObserver(Observer)`, `SetStore(Store)`, `Run(workflowRef string, ctx map[string]any) (entities.WorkflowResult, error)`, `Resume(pointer string, resolution map[string]any)`, `ResumeAs(pointer, identity string, resolution map[string]any)` |
| `llminjector` | `NewInjector() *Injector` | `RegisterSchema(schemaRef string, schema Schema, extractor Extractor)`, `SetObserver(Observer)`, `Extract(entities.ExtractionRequest) entities.ExtractionResult`, `ContextFrom(...)` |
| `sandbox` | `NewSandbox() *Sandbox` | `RegisterProbe(probeRef string, probe Probe)`, `Probe(entities.ProbeRequest) (entities.ProbeResult, error)` |
| `registry` | `NewRegistry(router *intentrouter.Router, engine *execution.Engine) *Registry` | `RegisterDomain(Domain)`, `Domains() []string` |
| `filestore` | `New(dir string) (*Store, error)` | `Save`, `Load`, `Consume`, `Release`, `Pending()`, `Orphaned(minAge)` |
| `scorecard` | `NewRecorder() *Recorder` | Accepted by `SetObserver`; `Snapshot(workflowID string) Scorecard`, `Scorecard.DeterminismRatio()` |
| `diagnostics` | `NewRecorder() *Recorder` | Accepted by `router.SetObserver` and `engine.SetObserver`; `ObserveRun()`, `Report()` with `IntentMissRate` / `ProbeRefusalRate` / `EscalationRate` |
| `httpapi` | `New(Config) http.Handler` | Mount it under your own middleware |
| `stdlib` | none (`Default() Library` returns the built-ins) | `std.set`, `std.require`, `std.rule_check`, `std.suspend`, `std.require_verified` |
| `manifest` | none (pure function package) | `Load(data []byte, hooks Hooks, std stdlib.Library) (*registry.Domain, error)`, `Validate(data []byte, std stdlib.Library) Report` |

## 7. Testing conventions (how to write them, not whether to — that's the normative handbook's business)

- Go convention: `xxx_test.go` sits in the same directory and package as the code under test. (Integration tests that
  need to verify multiple packages cooperating, like `registry_test.go`/`manifest_test.go`, are the exception; a
  separate `_test`-suffixed package is generally unnecessary.)
- Prefer the standard library `testing` plus `errors.As`/`errors.Is`. Do not introduce a third-party assertion library
  — the module stays dependency-free.
- Every package must cover at least one normal path and one boundary/unregistered-reference error path. The
  `execution` package additionally covers the breaker path and the sandbox gating path (see
  `TestFailureWithPolicyFallsBack` and `TestSandboxGateBlocksUnhealthyStepViaCircuitBreaker` in `engine_test.go`).

## 8. Common problems

- **`engine.Run` reports `no workflow registered for "xxx"`**: `WorkflowID` and `IntentNode.EntryWorkflowRef` must
  agree — what you pass to `Run` is the *WorkflowID*.
- **`cee validate` reports `entry_step_ref ... is deprecated`**: the field was renamed. It always held a
  `workflow_id`, and the old name `entry_step_ref` read as though it pointed at a step, which was simply wrong. The fix
  is to change the key; the value does not change at all:

  ```diff
  - {"node_id": "finance.dup", "entry_step_ref": "finance.flag_duplicate"}
  + {"node_id": "finance.dup", "entry_workflow_ref": "finance.flag_duplicate"}
  ```

  The old name still works (rule 3 forbids deleting a published JSON field); it just warns. Writing both names with
  different values is an outright error — that case cannot be resolved without guessing the author's intent. On the Go
  side the field is already `EntryWorkflowRef` and the old name no longer exists, so the compiler tells you.
- **`sandbox_probe_ref` declared but no probe registered**: `Engine.Run` returns a `no probe registered for "xxx"`
  error rather than silently skipping the gate. This is deliberate — "a gate was declared but the gate does nothing" is
  not allowed.
- **Manifest loading reports `references unregistered action_ref`**: check that the `Hooks` map key matches the JSON's
  `action_ref` exactly (case-sensitive).
