# httpsandbox — pre-execution sandbox on a remote/cloud service

A satellite module (own `go.mod`) that implements the core `execution.Prober`
by rehearsing a step's candidate command on a **remote** sandbox service over
HTTP, instead of in a local container. This is the shape a cloud sandbox — E2B,
Modal, or your own runner — takes: the CEE host needs no container runtime,
only network access.

It is the third isolation strategy alongside the other satellites, each
plugging into a different point of the engine:

| satellite | isolates | core interface |
|---|---|---|
| `dockersandbox` | your command, in a local throwaway container | `execution.Prober` |
| `httpsandbox` (this) | your command, on a remote/cloud sandbox | `execution.Prober` |
| `wasmhooks` | untrusted third-party *code* | `execution.Action` |

## The contract

```
POST {BaseURL}/rehearse
  request:  {"image": "<template>", "command": ["...", "..."]}
  response: {"exit_code": 0, "output": "..."}
```

Exit 0 is healthy; a non-zero exit, a non-200 status, or an unreachable service
is unhealthy — which the engine routes through the step's circuit breaker,
exactly like the in-process sandbox.

```go
engine := execution.NewEngine(httpsandbox.New(httpsandbox.Config{
    BaseURL: "https://sandbox.internal",
    APIKey:  os.Getenv("SANDBOX_TOKEN"),
    Image:   "python:3",
}))
```

## Pointing it at E2B / Modal / your own service

Two ways:

1. **A thin adapter** in front of the vendor: a small HTTP service that speaks
   the contract above and forwards to E2B/Modal. Nothing new in this module.
2. **The vendor's Go SDK directly**: write an alternate `New` that talks the
   SDK instead of HTTP. Add that SDK as a `require` **in this module's
   `go.mod`, never in the core** — the whole point of a satellite.

Everything here is tested offline with a fake HTTP client
(`httpsandbox_test.go`), including the end-to-end path where a failed remote
rehearsal gates a real migration step through the breaker.
