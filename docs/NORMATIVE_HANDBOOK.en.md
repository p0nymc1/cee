# CEE Normative Development Handbook

> 中文版：[NORMATIVE_HANDBOOK.md](NORMATIVE_HANDBOOK.md)

This handbook contains mandatory rules, not suggestions. The distinction: `DEVELOPMENT_GUIDE.en.md` teaches you how to
do things; this handbook specifies what is **not allowed**, and why. Because CEE is positioned as an open protocol any
industry can plug into, the rules must be written down and jointly enforced by all contributors — they cannot rest on
an unspoken assumption that "everyone knows the industry conventions."

Each rule states the consequence of violating it, so it can be cited during code review.

## 1. Architectural red lines (violation = merge rejected)

### 1.1 The LLM may extract, never decide

The return value of `llminjector.Extractor` may contain only **factual fields pulled from the source text** (amount,
date, entity name, …). It **must not contain any field of the form "what should happen next"** (is it anomalous,
should it be approved, severity level, …).

- **Why**: this is the core boundary separating CEE from a "fully autonomous LLM agent." The moment decision authority
  leaks into the extraction result, the deterministic execution engine degenerates into "the LLM decides and the code
  is a shell."
- **Enforcement**: `Injector.Extract` copies only the fields declared in `Schema` into `StructuredPayload`; anything
  outside the schema is silently dropped (see the `clean` construction in `injector.go`). **This rule is therefore
  enforced in practice by "do not declare decision fields in the Schema."** During code review, a `Schema` containing
  field names like `is_valid`, `should_alert`, or `severity_level` must be rejected.
- Shape of a legal field name: nominal, verifiable directly against the source text (`amount`, `merchant_name`,
  `invoice_id`). Shape of an illegal one: judgemental, requiring business rules to reach (`is_fraud`, `risk_score`,
  `action`).

#### 1.1.1 Clarifying the exception: why `std.rule_check` may produce judgement fields

`stdlib.std.rule_check` writes field names like `is_high_value` into its output — exactly the shape listed as illegal
above. This is not a violation, but **the boundary of the exemption must be understood clearly, or this exception will
be used to hollow out 1.1**:

- 1.1 constrains **who made the judgement**, not **what the judgement looks like**. What is forbidden is "the LLM makes
  the judgement and the code accepts it wholesale." A `std.rule_check` judgement is determined by the
  `field / op / value` triple hard-written in the manifest: pure code, fully reproducible, statically auditable
  (`cee validate` shows you exactly what it compares), with no LLM involved.
- The test is therefore **where the field's value came from**:
  - From an `llminjector` `Schema` → 1.1 applies; judgement fields are rejected outright.
  - From a deterministic computation in `stdlib` or a domain Go hook → allowed, because that *is* the business rule,
    and business rules were always meant to be carried by code.
- **The forbidden combination**: extracting a factual field with `llminjector` (legal) and then comparing it with
  `std.rule_check` is fine; but it is **not allowed** to have the extractor directly output an already-computed
  judgement and then use `std.set` to move it verbatim into the context to "launder" it. During code review, a
  `std.set` whose `fields` values are not literal constants but judgement fields sourced from an extraction result is
  treated as a violation of 1.1.

#### 1.1.2 An extraction is a "guess," not a "fact"

Rule 1.1 blocks "the LLM says what to do," but it does not block **reading one number wrong**. An extractor that reads
$50,000 as $5,000 made no decision, and yet made every decision — the deterministic rules downstream will very
confidently approve it automatically.

So `Injector.Extract` **structurally** stamps every field it produces with its provenance
(`ExtractionResult.ModelDerived`); the extractor cannot exempt itself. `llminjector.ContextFrom` carries that stamp
into the workflow context alongside the value (under the key `cee.model_derived`).

- **Why not a confidence score**: a model's self-reported confidence cannot be audited by the engine, and **a number
  nobody can verify is worse than no number — it manufactures false comfort.** Whereas "was this value guessed by a
  model" is a structural fact, 100% certain at the moment it is produced.
- **Consequential steps must guard themselves**: steps like transferring money, isolating a host, or disabling an
  account should use `std.require_verified` to declare which fields will not accept guessed values, failing through
  the circuit breaker to a human:

  ```json
  {"action_ref": "std.require_verified", "with": {"fields": ["amount", "account"]},
   "circuit_breaker_policy_ref": "needs_human_check"}
  ```

- **Laundering is forbidden (the same class of hole as 1.1.1)**: **no standard action that "marks a field as verified"
  exists, and none may be added.** A stamp in the manifest that says "verified" is a money-laundering tool — extract a
  number, stamp it, execute against it. Promoting a value from "guess" to "fact" may only be done by a Go hook, and
  only after it has **actually** consulted the authoritative system or asked a person. During code review, any hook
  that directly rewrites `cee.model_derived` is treated as a violation of this rule.
- **Callers must not merge extraction results by hand**: `ContextFrom` exists for exactly one reason — to make the
  correct approach less work than the incorrect one. Copying `StructuredPayload` straight into the context silently
  drops provenance, after which `std.require_verified` is decorative.

### 1.2 Sandbox probes must be read-only/simulated, with no real side effects

Functions registered via `sandbox.Probe` run during the rehearsal phase, **before the real step executes**. A probe may
not call any API that modifies external state (transfers, notifications, config changes, database writes, …).

- **Why**: the sandbox exists to "simulate first, then decide whether to really do it." If the probe itself has side
  effects, "rehearsal" is not a coherent concept — it is executing twice.
- **Code review checkpoint**: any call with `POST`/`PUT`/`DELETE` semantics inside a probe body, or any database write,
  is rejected outright. Permitted: read-only API calls, connectivity checks, dry-run mode APIs (provided the API's
  dry-run parameter is independently confirmed to produce no side effects).

### 1.3 Circuit breaker policies must be declared by named reference, never inlined

The `CircuitBreakerPolicyRef` on a `LeafStep`/`CompositeStep` must point at a policy registered via
`Engine.RegisterPolicy`. Hand-written `for`/retry loops, `time.Sleep` backoff, or hard-coded fallback logic inside an
`Action` function that bypasses this mechanism are **not allowed**.

- **Why**: centralised policy registration is the precondition for global auditability — a governance owner needs to be
  able to answer "how many safety nets exist in this system, and where does each fall back to." If retry logic is
  scattered inside individual `Action` bodies, that question can never be answered.
- **Code review checkpoint**: a loop plus `time.Sleep`/retry-counter pattern inside an `Action`/`Run` body is a
  violation; it should be refactored into "return an error on failure, and let `CircuitBreakerPolicyRef` take over."

### 1.4 No industry logic inside the engine packages

The eight packages `entities`, `execution`, `intentrouter`, `llminjector`, `sandbox`, `registry`, `manifest`, and
`stdlib` **may never import or hard-code any specific industry's concepts** (no `if domainID == "finance"` branches; no
field called `InvoiceAmount` instead of a generic `map[string]any`).

`stdlib` in particular must hold this line: a standard action library is naturally pulled at by "just one more action
and we could support scenario X." The test is that **no industry noun may appear in an action name or its
parameters** — `std.require` is legal (it only knows about "field, operator, value"); a hypothetical
`std.check_invoice_total` is not, and belongs in a domain plugin's own Go hook.

- **Why**: this is the literal meaning of "the engine knows references, not content." Once one industry's assumptions
  leak into an engine package, the next industry to plug in hits the "looks generic, actually only fits the first
  industry" problem.
- **Verification**: `TestTwoUnrelatedDomainsCoexistWithoutEngineChanges` in `registry/registry_test.go` is a living
  document — after any engine change, this test must still pass using two domains with entirely non-overlapping
  vocabularies (currently finance / security), without adding a special-case branch to the engine to make it pass.

### 1.5 Zero external dependencies in the core; dependencies live only in satellite modules

The core `cee` module (the repo root `go.mod`) **may depend only on the Go standard library** — `go.mod` must contain
no `require` entries. This is not fastidiousness; it is what makes the project propagate: anyone can `go build` without
trusting, auditing, or fetching third-party code.

Implementations needing heavyweight backends (container runtimes, E2B/cloud sandbox SDKs, WASM runtimes, vector
database clients, …) **may not enter the core**. They must live under `satellites/<name>/` with their own `go.mod`.
Because `go build ./...` does not descend into subdirectories with their own `go.mod`, a satellite's dependencies can
never reach the core. A satellite must plug in through an interface the core already exposes (`execution.Prober`,
`llminjector.Extractor`, `intentrouter.Vectorizer`, …) and must not require the core to change for it.

- **The test**: if it can be solved with the standard library plus an HTTP endpoint (as `llmhttp`/`embedhttp` do
  against OpenAI-compatible APIs), it stays in the core. If it must vendor an SDK, or needs CGO or a binary runtime,
  it goes to a satellite.
- **Code review checkpoint**: any PR adding a `require` to the root `go.mod` is rejected — convert it to a satellite
  module. `satellites/dockersandbox` is the reference template.

## 2. Naming conventions

| Identifier | Format | Example | Notes |
|---|---|---|---|
| `DomainID` | lowercase, single word or hyphenated | `finance`, `network-security` | Globally unique; two domains may not share a name |
| `IntentNode.NodeID` | `<domain>.<snake_case action>` | `finance.duplicate_expense` | The domain prefix keeps cross-domain collisions traceable to their source |
| `Workflow.WorkflowID` | `<domain>.<snake_case process>` | `finance.flag_duplicate` | **Also the value to put in `IntentNode.EntryWorkflowRef` / `entry_workflow_ref` in a manifest.** This field used to be called `EntryStepRef` / `entry_step_ref`, which was a misnomer — it has always held a workflow_id, never a step_id. It has been renamed; the old JSON name is still accepted per rule 3 but warns. **New manifests must use `entry_workflow_ref`.** |
| `Step.StepID` | `<snake_case action>`, unique within its workflow; no domain prefix needed | `check`, `notify`, `human_review` | Addressed only within its own workflow, so cross-domain uniqueness is unnecessary |
| A parallel step's `branches` | A list of `<domain>.<snake_case process>` | `["onboarding.credit_check", "onboarding.sanctions_check"]` | Holds workflow_ids, the same kind of value as `sub_workflow_ref`. **Branches must write distinct output fields**: two branches writing one field with different values is refused by the engine (specification 5.9.2), and is a shape to design away rather than discover at runtime |
| `CircuitBreakerPolicy.PolicyID` | `<snake_case policy intent>` | `escalate_to_review`, `security_containment_gate` | The name should express "what happens after a failure," not "which step uses it" — one policy may be referenced by many steps |
| `schema_ref` / `probe_ref` / `action_ref` | `<domain>.<snake_case name>` | `finance.expense_fields`, `finance.check_duplicate` | Domain-prefixed for the same reason as `NodeID` |

## 3. Manifest version compatibility rules

- When adding a field to `manifest.File`, it must use `omitempty` or have sensible zero-value semantics (the existing
  optional fields in `StepSpec` already follow this). A new field **may not** be made mandatory, or historical
  manifests will fail to parse outright instead of degrading gracefully.
- **Never** delete or rename an existing JSON field name in `manifest.File`/`IntentSpec`/`PolicySpec`/`WorkflowSpec`/
  `StepSpec` — these are the external contract, and renaming one breaks every published manifest. If deprecation is
  genuinely needed, mark it deprecated first and keep the field for at least one major version cycle.
- `Load` must return an `error` for any unrecognised field combination (unknown `type`, missing reference). It **may
  not** silently ignore it or fall back to a default — a mis-written manifest should fail at load time, not halfway
  through a run.

## 4. Contribution PR checklist

Self-check before submitting any PR that modifies the following:

- [ ] Did it touch the eight engine packages (`entities`/`execution`/`intentrouter`/`llminjector`/`sandbox`/`registry`/
      `manifest`/`stdlib`)? If so, does the two-domain test in `registry_test.go` still pass, without a new
      industry-specific branch (rule 1.4)?
- [ ] After adding/modifying a manifest, does `go run ./cmd/cee validate <manifest.json>` report no errors?
- [ ] When adding a standard action, is the action name and its parameters free of industry nouns (rule 1.4)? If it
      produces judgement fields, does it fall within the exemption boundary of rule 1.1.1?
- [ ] Are all new `Schema` fields factual, with no decision fields mixed in (rule 1.1)?
- [ ] Is each new `Probe` implementation read-only/simulated — such that a reviewer can confirm at a glance that it has
      no real side effects (rule 1.2)?
- [ ] Does each new `CircuitBreakerPolicyRef` point at a policy registered via `RegisterPolicy`, rather than a
      hand-written retry inside an `Action` (rule 1.3)?
- [ ] Do new identifiers (`NodeID`/`WorkflowID`/`PolicyID`/`*_ref`) follow the naming conventions in section 2?
- [ ] Were tests added for new paths: at least one success path plus one failure/unregistered-reference path?
- [ ] Do `go build ./...`, `go vet ./...`, and `go test ./...` all pass?
- [ ] Were any external dependencies introduced? `go.mod` being dependency-free is deliberate; a new dependency needs
      its own justification in the PR description and may not be slipped in alongside other work.

## 5. Error handling conventions

- Engine packages use native Go `error` throughout (including custom error types such as `CircuitBreakerTripped`).
  **`panic` is not used for normal business control flow** — `panic` is permitted only for genuine programming errors
  (an internal invariant being broken) and must be `recover`ed before the package boundary.
- Use `errors.As`/`errors.Is` when the specific error type matters (see how `engine_test.go` asserts on
  `*CircuitBreakerTripped`). Do not match on error message strings.
