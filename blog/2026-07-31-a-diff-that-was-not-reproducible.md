---
title: A determinism tool whose own output was not deterministic
date: 2026-07-31
summary: The regression diff printed its differences in a different order on every run, because it iterated a Go map. For the one package whose job is proving determinism, that is not cosmetic.
tags: replay, determinism
---

CEE's strongest claim is that you can replay last quarter's decisions against a proposed rule and
see which ones flip. Nothing else offers that, because an agent's plan exists only while it runs.

While building the demo for it, the output looked like this across two consecutive refunds:

```
r-0015   trace: ...   output.paid: true -> false   output.cee.failure_reason: ...   output.cee.failed_step: ...
r-0016   trace: ...   output.paid: true -> false   output.cee.failed_step: ...      output.cee.failure_reason: ...
```

Same shape of change, two different orderings. `Compare` walked `rec.Output` and `result.Output`
directly, and Go randomises map iteration on purpose. Every replay printed its differences in a
fresh order.

## Why this was worse than it looks

If this were a logging package, it would be a nuisance. Here it is the tool whose entire purpose
is demonstrating that the same input produces the same output. Handing someone a regression diff
that does not reproduce itself invites exactly one response, and it is the correct one.

It also breaks the use the feature exists for. "Run the diff before and after your change and
confirm nothing else moved" requires the two runs to be comparable as text. They were not.

## The fix, and the test that matters

Collect the union of field names, sort, then emit. Six lines.

The test is the part worth keeping:

```go
first := replay.Compare(rec, result, nil)
for run := 0; run < 50; run++ {
    got := replay.Compare(rec, result, nil)
    // every field, in the same position, fifty times
}
```

Fifty runs, because map iteration order is random rather than merely unspecified — a single
comparison passes by luck often enough to be useless as a guard.

## The one that was not a bug

The same demo surfaced something that looked like a second defect and was not. Replaying 37
refunds against a tighter limit reported 21 differences, but only 15 decisions had actually
changed. The other six were held before and are held now; the only thing different is the wording
of the reason handed to the operator.

Reporting 21 would have overstated the blast radius of the proposed rule change by forty percent —
and that number is exactly the kind someone carries into a decision meeting. The report now counts
flipped decisions and reworded explanations separately, and leads with the number that means
something.

That distinction was not in the original design. It only appeared because the demo was built
against a realistic batch rather than the two-line example the whitepaper had been carrying.
