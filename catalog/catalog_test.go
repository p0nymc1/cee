package catalog

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cee-project/cee/execution"
	"github.com/cee-project/cee/intentrouter"
	"github.com/cee-project/cee/manifest"
	"github.com/cee-project/cee/registry"
	"github.com/cee-project/cee/stdlib"
)

// TestRepoCatalogLintsClean guards the actual catalog shipped in this repo:
// every listed plugin must pass Lint. Because the catalog test runs with its
// own package directory as the working directory, "." is the catalog root.
func TestRepoCatalogLintsClean(t *testing.T) {
	cat, err := Load(".")
	if err != nil {
		t.Fatalf("cannot load repo catalog: %v", err)
	}
	if len(cat.Entries()) == 0 {
		t.Fatalf("repo catalog is empty")
	}
	report := cat.Lint(stdlib.Default())
	if !report.OK() {
		t.Fatalf("repo catalog does not lint clean:\n%s", report.String())
	}
}

// TestInstallAndRunFromRepoCatalog installs a real listed plugin and runs it,
// proving a no-code plugin travels from catalog to a live engine as data.
func TestInstallAndRunFromRepoCatalog(t *testing.T) {
	cat, err := Load(".")
	if err != nil {
		t.Fatalf("cannot load repo catalog: %v", err)
	}
	entry, ok := cat.Find("sla-guard")
	if !ok {
		t.Fatalf("expected sla-guard in the repo catalog")
	}

	dest := t.TempDir()
	path, err := cat.Install(entry, dest, stdlib.Default())
	if err != nil {
		t.Fatalf("install failed: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cannot read installed manifest: %v", err)
	}
	domain, err := manifest.Load(data, nil, stdlib.Default())
	if err != nil {
		t.Fatalf("installed manifest failed to load: %v", err)
	}

	router := intentrouter.NewRouter(0.3)
	engine := execution.NewEngine(nil)
	registry.NewRegistry(router, engine).RegisterDomain(*domain)

	met, err := engine.Run("sla-guard.evaluate", map[string]any{"response_minutes": 30.0})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if met.Output["sla_met"] != true {
		t.Fatalf("expected sla_met, got %+v", met.Output)
	}

	breached, err := engine.Run("sla-guard.evaluate", map[string]any{"response_minutes": 120.0})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if breached.Output["sla_breached"] != true {
		t.Fatalf("expected sla_breached via circuit breaker, got %+v", breached.Output)
	}
}

func TestFindMissingReturnsFalse(t *testing.T) {
	cat, _ := Load(".")
	if _, ok := cat.Find("does-not-exist"); ok {
		t.Fatalf("expected Find to report a missing plugin as absent")
	}
}

func TestLintCatchesBadManifestAndDuplicateName(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "index.json"), `{
		"plugins": [
			{"name": "dup", "tier": "L1", "version": "0.1.0", "domain": "dup", "manifest": "dup.json"},
			{"name": "dup", "tier": "L1", "version": "0.1.0", "domain": "dup", "manifest": "dup.json"},
			{"name": "broken", "tier": "L9", "version": "0.1.0", "domain": "broken", "manifest": "broken.json"}
		]
	}`)
	// dup.json is a valid no-code manifest; broken.json dangles on_success.
	writeFile(t, filepath.Join(root, "dup.json"), `{
		"name": "dup",
		"workflows": [{"workflow_id": "dup.wf", "entry_step_id": "a",
			"steps": [{"step_id": "a", "type": "leaf", "action_ref": "std.set", "with": {"fields": {"ok": true}}}]}]
	}`)
	writeFile(t, filepath.Join(root, "broken.json"), `{
		"name": "broken",
		"workflows": [{"workflow_id": "broken.wf", "entry_step_id": "a",
			"steps": [{"step_id": "a", "type": "leaf", "action_ref": "std.set", "with": {"fields": {"ok": true}}, "on_success": "nowhere"}]}]
	}`)

	cat, err := Load(root)
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	report := cat.Lint(stdlib.Default())
	if report.OK() {
		t.Fatalf("expected lint errors for duplicate name, bad tier, and dangling on_success")
	}
}

func TestInstallRefusesInvalidManifest(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "index.json"), `{
		"plugins": [{"name": "bad", "tier": "L1", "version": "0.1.0", "domain": "bad", "manifest": "bad.json"}]
	}`)
	writeFile(t, filepath.Join(root, "bad.json"), `{
		"name": "bad",
		"workflows": [{"workflow_id": "bad.wf", "entry_step_id": "missing", "steps": []}]
	}`)

	cat, _ := Load(root)
	entry, _ := cat.Find("bad")
	if _, err := cat.Install(entry, t.TempDir(), stdlib.Default()); err == nil {
		t.Fatalf("expected install to refuse a manifest that fails validation")
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
