<!-- Keep this short. The commit messages carry the detail. -->

**What this changes and why**


**Constraints (tick what applies, explain any you break)**
- [ ] `go build ./... && go vet ./... && go test ./...` pass
- [ ] `gofmt` clean
- [ ] If it touches an engine package, the two-domain test still passes and no industry logic was added
- [ ] If it adds a `require` to the root `go.mod` — it does not; heavyweight deps go in a satellite
- [ ] LLM stays extraction-only; execution stays deterministic
- [ ] New behaviour has a test, including a failure/edge path

**For a new plugin**
- [ ] `cee validate` and `cee lint` pass
- [ ] Ships a `benchmark.json`
