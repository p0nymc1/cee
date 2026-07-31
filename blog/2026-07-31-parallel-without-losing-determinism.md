---
title: Adding parallelism to an engine whose whole point is determinism
date: 2026-07-31
summary: Fan-out is easy. Fan-out that gives the same answer regardless of which branch finishes first, and refuses rather than arbitrates when two branches disagree, took longer.
tags: execution, determinism
---

Until this week the engine had exactly one outbound edge per step. Any process with independent
branches — run three screening checks, decide when they are all back — had to be flattened into a
sequence, or written by hand in a Go hook with its own goroutines.

The second option is the worse one, and not for the reason it first appears. Dropping into Go to
express a *shape* bypasses the no-code tier, and the no-code tier is the precondition for anyone
contributing a plugin without being a Go programmer. A missing primitive was quietly gatekeeping
the ecosystem.

## The actual difficulty

Running branches concurrently is a `sync.WaitGroup`. Running them concurrently in an engine that
sells determinism is the problem, because the obvious implementation loses it in three places at
once: merge order, trace order, and who wins a contested field.

The rule that resolved all three: **scheduling decides when work happens, never what the answer
is.**

- Branches join in declaration order, not completion order.
- Traces concatenate in declaration order, not completion order.
- Each branch starts from its own copy of the incoming context, so none can observe another.

That last one is not isolation for its own sake. It is what makes the first two true rather than
usually true — if branches could see each other's writes, the result would depend on which ran
first, and no amount of careful merging afterwards would fix it.

The test is a two-branch workflow with a deliberate delay in one branch, run twenty times,
requiring byte-identical traces.

## Refusing instead of arbitrating

Two branches writing the same field with different values could be resolved by declaration order.
That would be deterministic. It would also be wrong: nothing in the workflow says which should
win, so picking one is the engine inventing an answer.

It reports `*ConflictingBranchWrites` and refuses to join.

The subtlety is what counts as a conflict. A sub-workflow's output contains everything it
inherited, so comparing branch outputs directly flags "A changed `status`, B never touched it" as
a disagreement. The check has to diff each branch against the *incoming* context and compare only
the deltas. Same field with the same value is fine. One branch changing something another ignored
is fine.

## Two things that would have been regressions

**A panicking branch.** A panic inside a goroutine cannot be recovered by whoever called `Run` —
it takes the process down. Today, without fan-out, a panicking action unwinds to the caller, who
may well have a recover in their HTTP middleware. Adding parallelism would have silently converted
a recoverable fault into a process kill. Branches now recover and report `*BranchPanicked` naming
the branch, which preserves the existing blast radius.

**Observers.** They are now called from several goroutines at once. Both implementations in this
repo already hold mutexes, but that is luck rather than design, and it is now a documented
requirement rather than an implementation detail.

## The lock that had to land first

None of this was safe until the engine's workflow and policy registries stopped being unlocked
maps. Nothing was broken before — every caller registers everything before it starts serving — but
that safety came from caller discipline rather than from the type, and fan-out was about to break
the discipline.

The test for it earns its place by failing correctly: with the lock removed it reports a genuine
`WARNING: DATA RACE` under `-race`, rather than passing by luck. A concurrency test that cannot be
shown to fail is not evidence of anything.
