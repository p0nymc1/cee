# Getting started: turn a threshold rule into a replayable policy

For anyone who owns rules that decide things — an approval limit, a risk cutoff,
a routing threshold — and has to change them without being able to see, first,
what the change does to the decisions already made.

No Go. Everything here is a JSON file and the `cee` command. About ten minutes.

## The problem this solves

You have a rule somewhere that reads, in effect:

```
if amount <= 2000:  auto-approve
else:               send to manual review
```

Tightening `2000` to `1000` is a one-character change. The question nobody can
answer before it ships is: **which of last quarter's approvals would this have
sent to review instead?** That number is the risk of the change, and normally
you find it out in production.

CEE lets you compute it first.

## 1. Write the rule as a policy (2 min)

The engine has no `if`/`else`. A branch is a `std.require` step: if the
condition holds it takes `on_success`; if it fails, the circuit breaker routes
it to a fallback step. That pair *is* your if/else.

Save this as `txn-approval.json`:

```json
{
  "name": "txn-approval",
  "policies": [
    {"policy_id": "route_to_review", "fallback_step_ref": "manual_review"}
  ],
  "workflows": [{
    "workflow_id": "txn-approval.evaluate",
    "entry_step_id": "check_amount",
    "steps": [
      {"step_id": "check_amount", "type": "leaf", "action_ref": "std.require",
       "with": {"field": "amount", "op": "lte", "value": 2000},
       "circuit_breaker_policy_ref": "route_to_review", "on_success": "auto_approve"},
      {"step_id": "auto_approve", "type": "leaf", "action_ref": "std.set",
       "with": {"fields": {"decision": "auto_approved"}}},
      {"step_id": "manual_review", "type": "leaf", "action_ref": "std.set",
       "with": {"fields": {"decision": "manual_review"}}}
    ]
  }]
}
```

That is the whole rule. `check_amount` asserts `amount <= 2000`; holding it sets
`decision: auto_approved`, failing it routes to `manual_review`.

## 2. Check it (30 sec)

```bash
cee validate txn-approval.json
# ok: no issues
```

`validate` catches the mistakes that would otherwise surface at runtime —
a step that points at a fallback that does not exist, an `on_success` cycle, a
misspelled action. It is the same gate CI would run.

## 3. Bring a handful of past decisions (2 min)

Export a sample of real inputs you have already decided on, as an events file.
The shape is one object per historical case:

Save as `events.json`:

```json
{"events": [
  {"workflow_ref": "txn-approval.evaluate", "context": {"txn_id": "t1", "amount": 500}},
  {"workflow_ref": "txn-approval.evaluate", "context": {"txn_id": "t2", "amount": 900}},
  {"workflow_ref": "txn-approval.evaluate", "context": {"txn_id": "t3", "amount": 1500}},
  {"workflow_ref": "txn-approval.evaluate", "context": {"txn_id": "t4", "amount": 1800}},
  {"workflow_ref": "txn-approval.evaluate", "context": {"txn_id": "t5", "amount": 2500}},
  {"workflow_ref": "txn-approval.evaluate", "context": {"txn_id": "t6", "amount": 8000}}
]}
```

Six here to keep it readable; in practice use hundreds or thousands — a real
slice of history makes the answer in step 5 real.

## 4. Watch it decide (1 min)

The fastest way to see the policy run is the browser, no install at all:

**[p0nymc1.github.io/cee/playground](https://p0nymc1.github.io/cee/playground/)**
— paste your manifest into the left box and your events into the right, and each
input appears with the decision it produced and the path it took.

## 5. The payoff: change the rule, see what flips (2 min)

Copy the policy, tighten the limit, and diff. Make `txn-approval-tighter.json`
identical but with `"value": 1000`, then:

```bash
cee diff txn-approval.json txn-approval-tighter.json events.json
```

```
replayed 6 historical events against the proposed manifest
  2 of 6 decisions change

  event 2    amount=1500 txn_id=t3
      output.decision: auto_approved -> manual_review
  event 3    amount=1800 txn_id=t4
      output.decision: auto_approved -> manual_review

  2 more keep the same outcome; only the recorded reason or path changes
```

That is the whole point. Before shipping, you know the change moves exactly two
past transactions — the 1500 and the 1800 — from auto-approve to review, and you
know it by name. In the playground the same edit highlights those two rows live
as you type.

Two details that make the number trustworthy:

- It is a **deterministic replay**, so every difference is attributable to the
  rule change, not to anything moving underneath.
- A **changed decision** and a **changed explanation** are counted separately.
  The 2500 and 8000 are over both limits, so they are held either way; only the
  reason recorded for them changes. Folding those in would report "4 changed"
  and overstate the impact of your edit by double.

## 6. Make it automatic (optional, 2 min)

Put the policy and events in a repo and add the action, and every pull request
that edits the policy gets the flip report as a comment — so the review happens
before the merge, not after the incident:

```yaml
# .github/workflows/policy-diff.yml
on:
  pull_request:
    paths: ["policies/**/*.json"]
permissions:
  contents: read
  pull-requests: write
jobs:
  diff:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with: { fetch-depth: 0 }
      - uses: p0nymc1/cee@v0.2.0
        with:
          manifest: policies/txn-approval.json
          events: policies/txn-approval.events.json
```

## Where this stops, honestly

Everything above is the **no-code (L1) tier**: pure deterministic policies over
fields you supply. That is the tier `cee diff` and the playground work on, and
it covers a large share of real threshold-and-route rules.

It does **not** cover, on its own:

- **Fetching data** (looking a customer up, calling a scoring service). That is a
  side effect, and side effects live in a Go hook — the **L2 tier**.
- **Reading unstructured input** (a free-text claim, an email). That is the one
  job the LLM is allowed: extraction, into fields your policy then decides on.
- **Replaying policies that use probes or an LLM.** `cee diff` refuses a manifest
  that needs Go hooks rather than compare against half a domain; replaying those
  needs a recording (see `examples/rule_change`).

When you reach that edge, [`docs/DEVELOPMENT_GUIDE.md`](DEVELOPMENT_GUIDE.md)
picks up: how to add a Go hook, wire an extractor, and put a pre-execution probe
in front of a consequential step. The rules that decide, though, can stay in
JSON — reviewable, and replayable, by the people who own them.
