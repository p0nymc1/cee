---
name: Bug report
about: Something behaves differently from what the docs or a test claim
title: ""
labels: bug
assignees: ""
---

**What happened**
A clear description of the behaviour, and what you expected instead.

**Which package**
e.g. `execution`, `manifest`, `httpapi`, a satellite, or the CLI.

**Minimal reproduction**
The smallest manifest, workflow, or Go snippet that shows it. If it involves a
run, include the `Trace` and any `cee.failure_reason` from the output.

```
paste here
```

**Environment**
- `go version`:
- OS:
- CEE commit or tag:

**Anything else**
Logs, a failing test you wrote, or a guess at the cause.
