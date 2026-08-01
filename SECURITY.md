# Security Policy

## Reporting a vulnerability

Please report suspected vulnerabilities privately through GitHub's
[private security advisory](https://github.com/p0nymc1/cee/security/advisories/new)
form rather than opening a public issue. Include the affected package, a minimal
reproduction, and the impact you have in mind. You can expect an acknowledgement
and an initial assessment.

## Scope and posture

- The core module depends only on the Go standard library and vendors no
  third-party code, so its dependency-side attack surface is empty by
  construction. Heavyweight backends live in `satellites/`, each with its own
  `go.mod`, and are out of the core's supply chain.
- Untrusted input is expected in three places, and each has a defined boundary:
  a manifest loaded from a catalog (validated statically, paths resolved under
  the catalog root), a resume pointer arriving over HTTP (a capability, carried
  in the request body and never a URL), and a WASM hook (`satellites/wasmhooks`,
  run under a deadline and the runtime's isolation).
- The pre-execution sandbox is a security control, not a convenience: a probe
  runs read-only before a side-effecting step and can refuse it.

## Supported versions

This is pre-1.0 software. Fixes land on `main`; pin a commit or the `v0.1.0`
tag if you need stability.
