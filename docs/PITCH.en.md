# CEE — Pitch

> 中文版：[PITCH.md](PITCH.md)

> Each section is one slide. Technical detail is in the [whitepaper](WHITEPAPER.en.md); this deck covers three things
> only: **why now**, **what we built**, and **why you should believe it**.

---

## 1 · Opening: a real dilemma

Early one Tuesday morning, a security system detects a password spray against the VPN gateway.

**The detection is correct.** The attack is real, the source IP is unambiguous, confidence is 97%.

The automated response system blocks that IP, exactly per the standard playbook.

Three minutes later, **900 remote employees are offline** — that "attack source" was the company's own VPN egress
address.

---

## 2 · The problem isn't that the model wasn't smart enough

The industry's current answer is "use a stronger agent." But in the incident above:

- The detector produced **no false positive**
- The response logic was **not written wrong**
- Swap in a smarter model and **the outcome is identical**

Because the problem isn't the judgment. It's the **execution**.

> **A correct judgment can still have a catastrophic execution.**

This is why so many enterprises buy automation and then configure it to "alert only, never act" — **the capability was
purchased and nobody dares use it.**

---

## 3 · Neither existing path works

| | Traditional enterprise software | LLM agent |
|---|---|---|
| Optimises for | Zero failures, auditability | Zero friction, high intelligence |
| Costs you | Every process hand-coded | Black box, expensive, loops forever |
| Trusted with money and permissions | Yes | **No** |

Enterprises want a third path. And the key to that path is not making the model smarter — **it's using it somewhere
else.**

---

## 4 · Core insight: an agent is an interpreter, CEE is a compiler

Ask an agent the same thing ten thousand times and it **reasons ten thousand times**.

That thing was already worked out the first time.

| | Agent | CEE |
|---|---|---|
| When the model works | Re-reasons on every execution | **Reasons once** |
| What the model produces | An action that already happened | **A reviewable process** |
| Can you inspect it beforehand | **No** — the plan exists only during execution | Yes — the plan is a file |
| Cost of ten thousand runs | Ten thousand model calls | One |
| Reproducible when it goes wrong | No | Yes, verbatim |

**This is not about sending the model away. It's about letting it do the genuinely hard part — turning a messy
paragraph of human speech into a correct process — and doing it once.**

Following a process was never the model's job to begin with.

---

## 5 · How it actually works

```
You say:  "expenses over ten thousand need manager approval; never pay out on a closed account"
            ↓
          the model drafts a process
            ↓
          automatic static checks: infinite loops? references to steps that don't exist? wrong parameters?
            ↓
          replay against last year's real data: see what decisions it would make
            ↓
          you read it and nod
            ↓
After:    deterministic execution forever — auditable, reproducible, zero model cost
```

**The model is indispensable in this chain** — without it, a human has to draw the flowchart.

But it appears exactly once, and its output is **fully inspected before taking effect**. The model invents an
operation that doesn't exist? Rejected on the spot, without even the chance to be saved.

---

## 6 · One capability nobody else can offer

Step four above is not decoration. It is what this architecture uniquely enables:

> **Before you change a rule, compute which past decisions it would change.**

```
Tighten the approval limit from $100 to $50
Replay a refund of $80 that was already approved:
    approved  →  escalated to a human
```

Risk-model tuning, threshold tightening, policy changes — today these are all "ship it and see." Here they become
**see it before you ship.**

An agent cannot do this — **not because it isn't smart enough, but because its plan only exists during execution.**
There is nothing to inspect.

---

## 7 · Why believe it: open this right now

**https://p0nymc1.github.io/cee/**

This page is regenerated **hourly** by CI on a clean machine, and includes:

- Complete execution records for six categories of business scenario, showing which path every step took
- Live market monitoring (real data, not demo data)
- The full interception of the VPN incident from slide 1

**Not one word on that page is hand-written.** It is all real execution output — which is itself the argument: a
project claiming determinism should not have hand-maintained demo material.

---

## 8 · Current state

| | |
|---|---|
| Code | 10,213 lines, 25 packages, 220 tests (+17 satellite tests) |
| External dependencies | **0** (compiles offline; no third-party code to audit) |
| Open source | Apache-2.0, published |
| Scenario validation | 6 general categories + 2 real cases (network intrusion detection, market surveillance) |

Measured efficiency (reproducible, not estimated):

```
rank plugin           determinism  events     errors   LLM calls eliminated vs agent
1    access-review    100%         4          0        8 of 8
2    sla-guard        100%         4          0        8 of 8
```

How to read it: compared to an agent that calls the model at every step, these processes **eliminated every model
call**.

> Numbers like cost reduction and intent hit rate **require real production traffic**. We do not cite them before
> measuring them.

---

## 9 · What's missing (what the next stage buys)

Stated honestly, because it determines what the investment is for:

| Gap | Impact | Needs |
|---|---|---|
| Drafting path unvalidated against a real model | Logic and all four gates are tested (with stubs), but "does the model get it right first time" is unmeasured | One real integration and tuning pass |
| No identity source wired | The HTTP layer and engine authorisation are both in place and deny by default, but connecting real JWT/mTLS needs a reference implementation | An integration template |
| No parallelism | Real processes have parallel branches; without fan-out/fan-in they must be serialised or hand-written in Go — which bypasses the no-code tier | A fan-out/fan-in primitive |
| Metrics measure output only | Measures calls saved, not intents missed | Diagnostic metrics |
| **Zero ecosystem** | The catalog has 2 plugins, both ours. The whole community flywheel is built and unfuelled | Distribution, not code |

**The first two decide whether it can go to production. The last one decides whether it matters.**

---

## 10 · Boundaries and closing

Not overstating where this applies is itself part of being credible.

**Not suitable** for genuinely one-off exploration — "help me figure out what this error means." There is no
"compilation" value in that; an agent fits better.

**But what enterprises want to automate is not that kind of task.** It's **recurring** work. And recurrence is exactly
where compiling once pays off most.

> **Agents took the scenario that least needs repeated reasoning, and made it reason repeatedly every time.**

---

As model capabilities converge, the moat is no longer whose prompt is better written.

> **It's who can make an enterprise willing to hand permissions to automation.**

CEE's answer isn't a smarter model. It's **using the model somewhere else** — letting it design the process, rather
than pressing the execute button for you every single time.

---

*All numbers are version-controlled alongside the code and reproducible via CI or `make stats`.*
