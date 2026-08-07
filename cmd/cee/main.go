package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/p0nymc1/cee/bench"
	"github.com/p0nymc1/cee/catalog"
	"github.com/p0nymc1/cee/draft"
	"github.com/p0nymc1/cee/execution"
	"github.com/p0nymc1/cee/httpapi"
	"github.com/p0nymc1/cee/intentrouter"
	"github.com/p0nymc1/cee/llmhttp"
	"github.com/p0nymc1/cee/manifest"
	"github.com/p0nymc1/cee/policydiff"
	"github.com/p0nymc1/cee/registry"
	"github.com/p0nymc1/cee/stdlib"
	"net"
	"net/http"
	"time"
)

const defaultCatalogDir = "catalog"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "draft":
		os.Exit(runDraft(os.Args[2:]))
	case "serve":
		os.Exit(runServe(os.Args[2:]))
	case "validate":
		os.Exit(runValidate(os.Args[2:]))
	case "diff":
		os.Exit(runDiff(os.Args[2:]))
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
  cee draft "<description>"          draft a workflow from a description
  cee serve <manifest.json> [addr]   serve one manifest over HTTP (local trial)
  cee validate <manifest.json>       statically check one domain manifest
  cee diff <before> <after> <events> replay past decisions against a changed manifest
  cee lint [catalog_dir]             check a whole catalog's integrity
  cee list [catalog_dir]             list the plugins in a catalog
  cee install <name> [catalog_dir]   fetch a plugin manifest into ./plugins
  cee bench [catalog_dir]            run benchmarks and print a leaderboard

catalog_dir defaults to "catalog".

cee serve is for trying a manifest out, not a deployment model: it runs with no
authentication and an in-memory store, so it binds to loopback only. To deploy,
mount httpapi.New in your own service with a real Identify and a durable Store.

cee draft reads CEE_LLM_BASE_URL, CEE_LLM_MODEL and CEE_LLM_API_KEY. It prints
a manifest only once that manifest validates -- review it, then save it.
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

func runDiff(args []string) int {
	markdown := false
	failOnChange := false
	var positional []string
	for _, a := range args {
		switch a {
		case "--markdown":
			markdown = true
		case "--fail-on-change":
			failOnChange = true
		default:
			positional = append(positional, a)
		}
	}
	if len(positional) != 3 {
		fmt.Fprintln(os.Stderr,
			"usage: cee diff <before.json> <after.json> <events.json> [--markdown] [--fail-on-change]")
		return 2
	}

	before, err := os.ReadFile(positional[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "cee diff: %v\n", err)
		return 2
	}
	after, err := os.ReadFile(positional[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "cee diff: %v\n", err)
		return 2
	}
	eventsData, err := os.ReadFile(positional[2])
	if err != nil {
		fmt.Fprintf(os.Stderr, "cee diff: %v\n", err)
		return 2
	}
	suite, err := bench.ParseSuite(eventsData)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cee diff: %v\n", err)
		return 2
	}

	report, err := policydiff.Compare(before, after, suite, stdlib.Default())
	if err != nil {
		fmt.Fprintf(os.Stderr, "cee diff: %v\n", err)
		return 2
	}

	if markdown {
		fmt.Print(report.Markdown())
	} else {
		fmt.Print(report.Text())
	}

	// A changed decision is information, not an error, so the default exit code
	// is 0 -- a policy change is usually meant to change decisions. Gate a merge
	// on it only when the caller asks.
	if failOnChange && report.Flipped() > 0 {
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
			continue
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

func runDraft(args []string) int {
	if len(args) != 1 || strings.TrimSpace(args[0]) == "" {
		fmt.Fprintln(os.Stderr, `usage: cee draft "<description of the process>"`)
		return 2
	}

	baseURL := os.Getenv("CEE_LLM_BASE_URL")
	model := os.Getenv("CEE_LLM_MODEL")
	if baseURL == "" || model == "" {
		fmt.Fprintln(os.Stderr, "cee draft: set CEE_LLM_BASE_URL and CEE_LLM_MODEL")
		fmt.Fprintln(os.Stderr, "  e.g. CEE_LLM_BASE_URL=https://api.openai.com/v1 CEE_LLM_MODEL=gpt-4o-mini")
		return 2
	}

	cfg := draft.Config{
		LLM: llmhttp.Config{
			BaseURL: baseURL,
			Model:   model,
			APIKey:  os.Getenv("CEE_LLM_API_KEY"),
		},
	}

	result, err := draft.Draft(cfg, args[0], stdlib.Default())
	if err != nil {
		fmt.Fprintf(os.Stderr, "cee draft: %v\n", err)

		for i, attempt := range result.Attempts {
			if len(attempt.Manifest) > 0 {
				fmt.Fprintf(os.Stderr, "\n--- attempt %d ---\n%s\n", i+1, draft.Pretty(attempt.Manifest))
			}
		}
		return 1
	}

	os.Stdout.Write(draft.Pretty(result.Manifest))
	fmt.Fprintf(os.Stderr, "\nvalidated after %d attempt(s). Review it, then save and run:\n", len(result.Attempts))
	fmt.Fprintln(os.Stderr, "  cee validate <file>")
	return 0
}

func runServe(args []string) int {
	if len(args) < 1 || len(args) > 2 {
		fmt.Fprintln(os.Stderr, "usage: cee serve <manifest.json> [addr]")
		return 2
	}
	addr := "127.0.0.1:8080"
	if len(args) == 2 {
		addr = args[1]
	}
	if !isLoopback(addr) {
		fmt.Fprintf(os.Stderr,
			"cee serve: refusing to listen on %q.\n"+
				"It serves unauthenticated, so it binds to loopback only. To expose CEE, mount\n"+
				"httpapi.New in your own service with an Identify that authenticates.\n", addr)
		return 2
	}

	data, err := os.ReadFile(args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "cee serve: %v\n", err)
		return 2
	}

	domain, err := manifest.Load(data, nil, stdlib.Default())
	if err != nil {
		fmt.Fprintf(os.Stderr, "cee serve: %v\n", err)
		return 1
	}

	router := intentrouter.NewRouter(0.34)
	engine := execution.NewEngine(nil)
	store := execution.NewMemoryStore()
	engine.SetStore(store)
	registry.NewRegistry(router, engine).RegisterDomain(*domain)

	handler, err := httpapi.New(httpapi.Config{
		Engine: engine,

		AllowAnonymous: true,
		Pending:        httpapi.MemoryPending{Store: store},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "cee serve: %v\n", err)
		return 1
	}

	fmt.Printf("serving %q on http://%s\n", domain.Name, addr)
	fmt.Println("  unauthenticated, in-memory, loopback only — for trying it out")
	for _, wf := range domain.Workflows {
		fmt.Printf("\n  curl -s http://%s/v1/run -d '{\"workflow\":%q,\"context\":{}}'\n", addr, wf.WorkflowID)
	}
	fmt.Printf("\n  curl -s http://%s/v1/pending\n", addr)

	server := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	if err := server.ListenAndServe(); err != nil {
		fmt.Fprintf(os.Stderr, "cee serve: %v\n", err)
		return 1
	}
	return 0
}

func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
