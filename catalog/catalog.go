package catalog

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/p0nymc1/cee/manifest"
	"github.com/p0nymc1/cee/stdlib"
)

type Entry struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Version     string   `json:"version"`
	Tier        string   `json:"tier"`
	Domain      string   `json:"domain"`
	Manifest    string   `json:"manifest"`
	Benchmark   string   `json:"benchmark,omitempty"`
	Tags        []string `json:"tags,omitempty"`
}

type indexFile struct {
	Plugins []Entry `json:"plugins"`
}

type Catalog struct {
	root    string
	entries []Entry
}

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

func (c *Catalog) Entries() []Entry {
	return c.entries
}

func (c *Catalog) Find(name string) (Entry, bool) {
	for _, e := range c.entries {
		if e.Name == name {
			return e, true
		}
	}
	return Entry{}, false
}

func (c *Catalog) ManifestPath(e Entry) string {
	return filepath.Join(c.root, e.Manifest)
}

func (c *Catalog) ReadManifest(e Entry) ([]byte, error) {
	return os.ReadFile(c.ManifestPath(e))
}

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

func manifestName(data []byte) (string, bool) {
	var head struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return "", false
	}
	return head.Name, head.Name != ""
}
