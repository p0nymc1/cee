// Package catalog is CEE's community distribution layer: a git-based index
// of publishable domain plugins. There is deliberately no service and no
// database -- a catalog is just an index.json plus the manifest files it
// points at, so contributing a plugin is a pull request and nothing more.
// This is the "start dead simple, grow by PR" stage of the plugin
// ecosystem; a hosted registry can come later behind the same Entry shape.
//
// The catalog carries L1 (pure-manifest, no-code) plugins, which can be
// fetched and run as data. L2 plugins that need Go hooks are distributed as
// Go modules instead; an Entry may still describe one (Tier "L2") for
// discovery, but Install only handles manifests it can fully validate.
package catalog

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/cee-project/cee/manifest"
	"github.com/cee-project/cee/stdlib"
)

// Entry is one plugin's listing in the catalog index.
type Entry struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Version     string   `json:"version"`
	Tier        string   `json:"tier"`
	Domain      string   `json:"domain"`
	Manifest    string   `json:"manifest"`            // path relative to the catalog root
	Benchmark   string   `json:"benchmark,omitempty"` // optional standard-events fixture, relative to root
	Tags        []string `json:"tags,omitempty"`
}

type indexFile struct {
	Plugins []Entry `json:"plugins"`
}

// Catalog is a loaded index plus the root directory its manifest paths are
// resolved against.
type Catalog struct {
	root    string
	entries []Entry
}

// Load reads root/index.json. It does not validate the manifests -- call
// Lint for that.
func Load(root string) (*Catalog, error) {
	data, err := os.ReadFile(filepath.Join(root, "index.json"))
	if err != nil {
		return nil, fmt.Errorf("catalog: %w", err)
	}
	var idx indexFile
	if err := json.Unmarshal(data, &idx); err != nil {
		return nil, fmt.Errorf("catalog: invalid index.json: %w", err)
	}
	return &Catalog{root: root, entries: idx.Plugins}, nil
}

// Entries returns the listed plugins in index order.
func (c *Catalog) Entries() []Entry {
	return c.entries
}

// Find looks up an entry by name.
func (c *Catalog) Find(name string) (Entry, bool) {
	for _, e := range c.entries {
		if e.Name == name {
			return e, true
		}
	}
	return Entry{}, false
}

// ManifestPath resolves an entry's manifest to a filesystem path.
func (c *Catalog) ManifestPath(e Entry) string {
	return filepath.Join(c.root, e.Manifest)
}

// ReadManifest reads an entry's manifest bytes.
func (c *Catalog) ReadManifest(e Entry) ([]byte, error) {
	return os.ReadFile(c.ManifestPath(e))
}

// ReadBenchmark reads an entry's benchmark fixture bytes. The second return
// is false when the entry declares no benchmark, which is not an error.
func (c *Catalog) ReadBenchmark(e Entry) ([]byte, bool, error) {
	if e.Benchmark == "" {
		return nil, false, nil
	}
	data, err := os.ReadFile(filepath.Join(c.root, e.Benchmark))
	if err != nil {
		return nil, true, err
	}
	return data, true, nil
}

// Lint checks the whole catalog's integrity so a contributor's PR can be
// gated in CI: unique names, a known tier, a manifest that exists, whose
// declared name matches the entry, and that passes manifest.Validate. It
// reuses manifest.Report so `cee lint` and `cee validate` speak the same
// language.
func (c *Catalog) Lint(std stdlib.Library) manifest.Report {
	var report manifest.Report
	seen := map[string]bool{}

	for _, e := range c.entries {
		prefix := fmt.Sprintf("plugin %q", e.Name)

		if e.Name == "" {
			report.Issues = append(report.Issues, manifest.Issue{Severity: manifest.Error, Message: "an entry is missing a name"})
			continue
		}
		if seen[e.Name] {
			report.Issues = append(report.Issues, manifest.Issue{Severity: manifest.Error, Message: fmt.Sprintf("%s: duplicate entry name", prefix)})
		}
		seen[e.Name] = true

		if e.Tier != "L1" && e.Tier != "L2" {
			report.Issues = append(report.Issues, manifest.Issue{Severity: manifest.Error, Message: fmt.Sprintf("%s: tier %q must be L1 or L2", prefix, e.Tier)})
		}
		if e.Version == "" {
			report.Issues = append(report.Issues, manifest.Issue{Severity: manifest.Warning, Message: fmt.Sprintf("%s: no version", prefix)})
		}
		if e.Manifest == "" {
			report.Issues = append(report.Issues, manifest.Issue{Severity: manifest.Error, Message: fmt.Sprintf("%s: no manifest path", prefix)})
			continue
		}

		data, err := c.ReadManifest(e)
		if err != nil {
			report.Issues = append(report.Issues, manifest.Issue{Severity: manifest.Error, Message: fmt.Sprintf("%s: cannot read manifest: %v", prefix, err)})
			continue
		}

		if name, ok := manifestName(data); ok && e.Domain != "" && name != e.Domain {
			report.Issues = append(report.Issues, manifest.Issue{Severity: manifest.Warning, Message: fmt.Sprintf("%s: entry domain %q does not match manifest name %q", prefix, e.Domain, name)})
		}

		sub := manifest.Validate(data, std)
		for _, issue := range sub.Issues {
			report.Issues = append(report.Issues, manifest.Issue{
				Severity: issue.Severity,
				Message:  fmt.Sprintf("%s: %s", prefix, issue.Message),
			})
		}
	}

	return report
}

// Install validates an entry's manifest and, only if it is error-free,
// copies it to destDir/<name>.json. Validation is the install-time quality
// gate: a plugin that would not load is never placed on disk.
func (c *Catalog) Install(e Entry, destDir string, std stdlib.Library) (string, error) {
	data, err := c.ReadManifest(e)
	if err != nil {
		return "", fmt.Errorf("catalog: cannot read manifest for %q: %w", e.Name, err)
	}

	report := manifest.Validate(data, std)
	if !report.OK() {
		return "", fmt.Errorf("catalog: refusing to install %q, its manifest has errors:\n%s", e.Name, report.String())
	}

	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", fmt.Errorf("catalog: %w", err)
	}
	dest := filepath.Join(destDir, e.Name+".json")
	if err := os.WriteFile(dest, data, 0o644); err != nil {
		return "", fmt.Errorf("catalog: %w", err)
	}
	return dest, nil
}

// manifestName pulls just the "name" field out of a manifest without a full
// parse, for the entry/manifest cross-check.
func manifestName(data []byte) (string, bool) {
	var head struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return "", false
	}
	return head.Name, head.Name != ""
}
