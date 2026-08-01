package catalog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/p0nymc1/cee/stdlib"
)

// A catalog is a third-party distribution unit, so its index.json is untrusted.
// These tests pin that a hostile index cannot read outside the catalog root or
// write outside the install directory.

func writeCatalog(t *testing.T, index string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.json"), []byte(index), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestReadManifestRefusesAPathEscapingTheRoot(t *testing.T) {
	root := writeCatalog(t, `{"plugins":[{"name":"evil","tier":"L1","manifest":"../secret.txt"}]}`)
	if err := os.WriteFile(filepath.Join(root, "..", "secret.txt"), []byte("top secret"), 0o600); err != nil {
		t.Fatal(err)
	}

	cat, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	entry, _ := cat.Find("evil")
	if _, err := cat.ReadManifest(entry); err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("reading ../secret.txt must be refused, got %v", err)
	}
}

func TestInstallRefusesANameThatEscapesTheDestination(t *testing.T) {
	root := writeCatalog(t, `{"plugins":[{"name":"../../pwned","tier":"L1","manifest":"m.json"}]}`)
	if err := os.WriteFile(filepath.Join(root, "m.json"), []byte(`{"name":"x","workflows":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}

	cat, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	entry := cat.Entries()[0]
	if _, err := cat.Install(entry, filepath.Join(root, "plugins"), stdlib.Default()); err == nil {
		t.Fatal("installing a plugin whose name escapes the destination must be refused")
	}
	if _, err := os.Stat(filepath.Join(root, "..", "pwned.json")); err == nil {
		t.Fatal("a file was written outside the install directory")
	}
}

func TestLintFlagsAHostileName(t *testing.T) {
	root := writeCatalog(t, `{"plugins":[{"name":"../../evil","tier":"L1","manifest":"m.json"}]}`)
	cat, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if cat.Lint(stdlib.Default()).OK() {
		t.Fatal("lint must reject a hostile plugin name")
	}
}

func TestAWellFormedRelativePathStillWorks(t *testing.T) {
	root := writeCatalog(t, `{"plugins":[{"name":"ok","tier":"L1","manifest":"plugins/ok/manifest.json"}]}`)
	mdir := filepath.Join(root, "plugins", "ok")
	if err := os.MkdirAll(mdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mdir, "manifest.json"), []byte(`{"name":"ok","workflows":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}

	cat, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	entry, _ := cat.Find("ok")
	if _, err := cat.ReadManifest(entry); err != nil {
		t.Fatalf("a legitimate relative manifest path must still read: %v", err)
	}
}
