---
name: Feature request
about: Propose a capability, and where it should sit
title: ""
labels: enhancement
assignees: ""
---

**The process you want to express**
Describe the real workflow, not the API. What decision is being made, and where
does a human or an external system come in?

**Why the current primitives do not cover it**
Which of leaf / composite / parallel steps, `std.*` actions, probes, circuit
breakers, or suspend/resume you tried, and where they fell short.

**Where it belongs**
- [ ] core engine (`execution`) — a new step shape or execution rule
- [ ] standard library (`stdlib`) — a deterministic, industry-agnostic action
- [ ] a satellite module — needs a heavyweight dependency
- [ ] a plugin — expressible as a manifest, no engine change
- [ ] not sure

**Constraint check**
Does it keep the LLM to extraction only, keep the core dependency-free, and keep
execution deterministic? If it does not, say why the trade-off is worth it.
