# Contributing to CEE

Contributions are welcome. CEE is positioned as an open protocol any industry can plug into, so the collaboration
rules are deliberately written down rather than left to shared assumption. This guide tells you **how to start**; the
mandatory red lines are in [`docs/NORMATIVE_HANDBOOK.md`](docs/NORMATIVE_HANDBOOK.md), and the hands-on detail is
in [`docs/DEVELOPMENT_GUIDE.md`](docs/DEVELOPMENT_GUIDE.md).

## Two kinds of contribution

**A. Contribute a domain plugin** (the most common, and the most welcome) — you don't need to change the engine, only
describe your process.

**B. Improve the engine itself** — the business-agnostic core packages `entities` / `execution` / `intentrouter` /
`llminjector` / `sandbox` / `registry` / `manifest` / `stdlib`. The bar is higher here, because they are the contract
every plugin depends on.

## Before you start

```bash
go build ./... && go vet ./... && go test ./...   # all three must be green
```

Requires **Go 1.26+** (`go.mod` declares `go 1.26.5`, which is a hard floor).

The repo having **zero external dependencies** is deliberate — `go.mod` has no `require` entries. A new dependency
needs its own justification in the PR description and may not be slipped in alongside other work.

## A. Contributing a plugin

### L1: no code (pure JSON)

If your process can be expressed with the standard action library (`std.set` / `std.require` / `std.rule_check` /
`std.suspend` / `std.require_verified`), you don't need to write Go at all:

1. Write your manifest at `catalog/plugins/<your-plugin-name>/manifest.json` (see
   `catalog/plugins/sla-guard/manifest.json` for the structure).
2. Check it yourself with the validator — this is the admission gate; don't open a PR that fails it:
   ```bash
   go run ./cmd/cee validate catalog/plugins/<name>/manifest.json
   ```
3. Add an entry to `catalog/index.json` (`name` / `tier: "L1"` / `version` / `domain` / `manifest` path).
4. Validate the catalog as a whole:
   ```bash
   go run ./cmd/cee lint      # must print ok: no issues
   ```
5. (Optional but recommended) Add a `benchmark.json` standard event set and a `benchmark` field in your entry, so your
   plugin appears on the leaderboard:
   ```bash
   go run ./cmd/cee bench
   ```

How to write a branch: the engine has no if/else. Use `std.require` — the condition holding takes `on_success`, and it
not holding fails the step, which `circuit_breaker_policy_ref` routes to a fallback step. To "park and wait for a human
or a callback," use `std.suspend`.

### L2: with code (manifest + Go hooks)

For logic the standard actions cannot express (touching an external system, say), point `action_ref` at a named Go
function (`manifest.Hooks`). L2 plugins are distributed as Go modules rather than through the catalog's `install`, but
can be registered in the index with `tier: "L2"` so they are discoverable. See section 3 of the development guide.

## B. Improving the engine

Before changing a core package, read the four architectural red lines in section 1 of
[`docs/NORMATIVE_HANDBOOK.md`](docs/NORMATIVE_HANDBOOK.md). **Violation means the merge is rejected.** In
summary:

1. **The LLM may extract, never decide** — judgement fields like `is_fraud`/`should_alert` are not allowed in a
   `Schema`.
2. **Sandbox probes are read-only** — no real side effects, ever.
3. **Circuit breakers go through named policies** — no hand-written retry loops inside an action.
4. **No industry logic in engine packages** — no `if domainID == "finance"` branches, and no industry nouns in
   `stdlib` action names or parameters.

The guard test: `TestTwoUnrelatedDomainsCoexistWithoutEngineChanges` in `registry/registry_test.go` verifies "the
engine contains no industry logic" using two domains with entirely non-overlapping vocabularies. After your change it
must still pass, and you must not have added a special-case branch to the engine to make it pass.

## PR checklist

Self-check each item before submitting (the full version is in section 4 of the normative handbook):

- [ ] `go build ./...` / `go vet ./...` / `go test ./...` all green, `gofmt` clean.
- [ ] New plugin: passes `cee validate`; if it goes into the catalog, `cee lint` is clean.
- [ ] Touched an engine core package: all four red lines held, and the two-domain guard test still passes.
- [ ] New paths have tests: at least one success path plus one failure/unregistered-reference path.
- [ ] Naming follows section 2 of the normative handbook (the domain-prefix conventions for
      `NodeID`/`WorkflowID`/`PolicyID`/`*_ref`).
- [ ] No external dependency slipped in (if one is needed, justify it separately in the PR description).

## Code style

- Official Go style; run `gofmt -w` before committing.
- Tests use the standard library `testing` plus `errors.As`/`errors.Is`; no third-party assertion libraries.
- Error handling uses native `error`; `panic` is only for genuine programming errors.

## Code of conduct

Kind to people, strict about the work. Reviews address code, not people; disagreements are settled with tests and
data.

## License

By submitting a contribution you agree to license it under this project's [Apache License 2.0](LICENSE).
