// Command cee is the CEE command-line tool for authoring and distributing
// domain plugins:
//
//	cee validate <manifest.json>     statically check one manifest
//	cee lint [catalog_dir]           check a whole catalog's integrity (CI gate)
//	cee list [catalog_dir]           list the plugins in a catalog
//	cee install <name> [catalog_dir] fetch a plugin manifest into ./plugins
//
// validate and lint turn the normative handbook's red lines into automated
// gates, so a contributor never has to wait on a human reviewer to know a
// plugin is well-formed.
package main

import (
	"fmt"
	"os"

	"github.com/cee-project/cee/bench"
	"github.com/cee-project/cee/catalog"
	"github.com/cee-project/cee/manifest"
	"github.com/cee-project/cee/stdlib"
)

const defaultCatalogDir = "catalog"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "validate":
		os.Exit(runValidate(os.Args[2:]))
	case "lint":
		os.Exit(runLint(os.Args[2:]))
	case "list":
		os.Exit(runList(os.Args[2:]))
	case "install":
		os.Exit(runInstall(os.Args[2:]))
	case "bench":
		os.Exit(runBench(os.Args[2:]))
	case "-h", "--help", "help":
		usage()
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "cee: unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `cee - Cognitive Execution Engine tooling

usage:
  cee validate <manifest.json>       statically check one domain manifest
  cee lint [catalog_dir]             check a whole catalog's integrity
  cee list [catalog_dir]             list the plugins in a catalog
  cee install <name> [catalog_dir]   fetch a plugin manifest into ./plugins
  cee bench [catalog_dir]            run benchmarks and print a leaderboard

catalog_dir defaults to "catalog".
`)
}

func runValidate(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: cee validate <manifest.json>")
		return 2
	}
	data, err := os.ReadFile(args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "cee validate: %v\n", err)
		return 2
	}
	report := manifest.Validate(data, stdlib.Default())
	fmt.Println(report.String())
	if !report.OK() {
		return 1
	}
	return 0
}

func runLint(args []string) int {
	dir := defaultCatalogDir
	if len(args) == 1 {
		dir = args[0]
	} else if len(args) > 1 {
		fmt.Fprintln(os.Stderr, "usage: cee lint [catalog_dir]")
		return 2
	}
	cat, err := catalog.Load(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cee lint: %v\n", err)
		return 2
	}
	report := cat.Lint(stdlib.Default())
	fmt.Println(report.String())
	if !report.OK() {
		return 1
	}
	return 0
}

func runList(args []string) int {
	dir := defaultCatalogDir
	if len(args) == 1 {
		dir = args[0]
	} else if len(args) > 1 {
		fmt.Fprintln(os.Stderr, "usage: cee list [catalog_dir]")
		return 2
	}
	cat, err := catalog.Load(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cee list: %v\n", err)
		return 2
	}
	entries := cat.Entries()
	if len(entries) == 0 {
		fmt.Println("(catalog is empty)")
		return 0
	}
	for _, e := range entries {
		fmt.Printf("%-16s %-4s v%-8s %s\n", e.Name, e.Tier, e.Version, e.Description)
	}
	return 0
}

func runInstall(args []string) int {
	if len(args) < 1 || len(args) > 2 {
		fmt.Fprintln(os.Stderr, "usage: cee install <name> [catalog_dir]")
		return 2
	}
	name := args[0]
	dir := defaultCatalogDir
	if len(args) == 2 {
		dir = args[1]
	}
	cat, err := catalog.Load(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cee install: %v\n", err)
		return 2
	}
	entry, ok := cat.Find(name)
	if !ok {
		fmt.Fprintf(os.Stderr, "cee install: no plugin named %q in %s\n", name, dir)
		return 1
	}
	dest, err := cat.Install(entry, "plugins", stdlib.Default())
	if err != nil {
		fmt.Fprintf(os.Stderr, "cee install: %v\n", err)
		return 1
	}
	fmt.Printf("installed %q -> %s\n", name, dest)
	return 0
}

func runBench(args []string) int {
	dir := defaultCatalogDir
	if len(args) == 1 {
		dir = args[0]
	} else if len(args) > 1 {
		fmt.Fprintln(os.Stderr, "usage: cee bench [catalog_dir]")
		return 2
	}
	cat, err := catalog.Load(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cee bench: %v\n", err)
		return 2
	}

	var results []bench.Result
	for _, entry := range cat.Entries() {
		benchData, ok, err := cat.ReadBenchmark(entry)
		if err != nil {
			fmt.Fprintf(os.Stderr, "cee bench: %q: %v\n", entry.Name, err)
			return 1
		}
		if !ok {
			continue // no benchmark fixture: skip, not an error
		}
		suite, err := bench.ParseSuite(benchData)
		if err != nil {
			fmt.Fprintf(os.Stderr, "cee bench: %q: %v\n", entry.Name, err)
			return 1
		}
		manifestData, err := cat.ReadManifest(entry)
		if err != nil {
			fmt.Fprintf(os.Stderr, "cee bench: %q: %v\n", entry.Name, err)
			return 1
		}
		domain, err := manifest.Load(manifestData, nil, stdlib.Default())
		if err != nil {
			fmt.Fprintf(os.Stderr, "cee bench: %q: %v\n", entry.Name, err)
			return 1
		}
		results = append(results, bench.Run(*domain, suite))
	}

	if len(results) == 0 {
		fmt.Println("(no benchmarked plugins in catalog)")
		return 0
	}
	fmt.Print(bench.FormatLeaderboard(results))
	return 0
}
