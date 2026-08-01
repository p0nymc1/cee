package main

import (
	"bytes"
	"fmt"
	"html"
	"html/template"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type page struct {
	Title       string
	Description string
	Nav         string
	Root        string
	Repo        string
	Short       string
	Built       string
	Styles      template.CSS
	Body        template.HTML
}

type site struct {
	out   string
	repo  string
	short string
	built string
	shell *template.Template
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "site:", err)
		os.Exit(1)
	}
}

func run() error {
	commit := envOr("GITHUB_SHA", "local")
	short := commit
	if len(short) > 7 {
		short = short[:7]
	}

	s := &site{
		out:   envOr("SITE_OUT", "site"),
		repo:  envOr("GITHUB_REPOSITORY", "p0nymc1/cee"),
		short: short,
		built: time.Now().UTC().Format("2006-01-02 15:04 UTC"),
		shell: template.Must(template.New("shell").Parse(shell)),
	}

	board, err := loadBoard("demo-output/bench.txt", "catalog/index.json")
	if err != nil {
		return err
	}

	posts, err := loadPosts("blog")
	if err != nil {
		return err
	}

	if err := s.writeHome(board); err != nil {
		return err
	}
	if err := s.writeBoard(board); err != nil {
		return err
	}
	if err := s.writeBlog(posts); err != nil {
		return err
	}

	fmt.Printf("built %s: home, leaderboard (%d ranked), blog (%d posts)\n",
		s.out, len(board.Entries), len(posts))
	return nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func (s *site) render(path string, p page) error {
	p.Repo, p.Short, p.Built = s.repo, s.short, s.built
	p.Styles = template.CSS(styles)
	if p.Description == "" {
		p.Description = "A business-agnostic protocol for deterministic-first execution, where the LLM is an edge tool rather than the driver."
	}

	var buf bytes.Buffer
	if err := s.shell.Execute(&buf, p); err != nil {
		return err
	}

	full := filepath.Join(s.out, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	return os.WriteFile(full, buf.Bytes(), 0o644)
}

func (s *site) renderBody(name, tmpl string, data any) (template.HTML, error) {
	t, err := template.New(name).Parse(tmpl)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", err
	}
	return template.HTML(buf.String()), nil
}

func (s *site) writeBoard(board Board) error {
	body, err := s.renderBody("board", boardTemplate, struct {
		Board Board
		Short string
		Repo  string
	}{board, s.short, s.repo})
	if err != nil {
		return err
	}
	return s.render("leaderboard/index.html", page{
		Title:       "Plugin leaderboard — CEE",
		Description: "Every CEE plugin ranked by how many model calls it removes, measured by cee bench on a clean runner.",
		Nav:         "board",
		Root:        "../",
		Body:        body,
	})
}

func (s *site) writeBlog(posts []Post) error {
	body, err := s.renderBody("blogindex", blogIndexTemplate, struct {
		Posts []Post
		Root  string
	}{posts, "../"})
	if err != nil {
		return err
	}
	if err := s.render("blog/index.html", page{
		Title:       "Blog — CEE",
		Description: "Notes on building a deterministic execution engine.",
		Nav:         "blog",
		Root:        "../",
		Body:        body,
	}); err != nil {
		return err
	}

	for _, post := range posts {
		body, err := s.renderBody("post", postTemplate, struct {
			Post Post
			Root string
		}{post, "../../"})
		if err != nil {
			return err
		}
		description := post.Summary
		if description == "" {
			description = post.Title
		}
		if err := s.render(filepath.Join("blog", post.Slug, "index.html"), page{
			Title:       post.Title + " — CEE",
			Description: description,
			Nav:         "blog",
			Root:        "../../",
			Body:        body,
		}); err != nil {
			return err
		}
	}
	return nil
}

type section struct {
	Heading string
	Prose   string
	Capture string
}

var sections = []section{
	{"Changing a rule, before you ship it",
		`The dividend of a deterministic engine, and the one thing no agent can offer: replay last
quarter's decisions against a proposed rule and see exactly which ones flip. The plan an agent
follows exists only while it runs, so there is nothing to replay. Note that one refund inside
the affected range does not flip — probe verdicts come from the recording rather than a live
check, so an account that was closed at the time stays closed, and the rule is the only thing
that moved.`,
		"rule_change"},

	{"Code audit — an AI PR reviewer, re-cast",
		`The loop CodeRabbit, GitHub Copilot code review and Qodo PR-Agent run — find an issue and decide what to do
about it — with the decision taken back from the model. The model only extracts a finding into structured fields,
stamped model-derived; a deterministic workflow classifies severity and refuses to auto-block on a guessed one. A
sandbox probe rehearses the action first, so blocking the hotfix that ends an incident, or autofixing generated code,
routes to a human instead of executing. The final line is the error side of the batch: intent miss, probe refusal.`,
		"code_audit"},

	{"Crypto market surveillance — live data",
		`Fixed thresholds over live quotes, swept hourly. This is anomaly flagging, not investment
advice: every rule is a constant in a manifest or in deterministic Go, and no model is asked
whether anything is worth buying. The guardrail is that a correct rule can still fire on data
not worth acting on — a quote minutes stale describes a market that has moved on, and a large
percentage move on a thin book is noise. A probe checks both before anything is raised.`,
		"crypto_surveillance"},

	{"Network intrusion detection",
		`Alerts are matched to MITRE ATT&CK techniques and contained automatically — unless
containment would do more damage than the intrusion. This is the reason detection teams
distrust automated response: the dangerous case is not a wrong detector, it is a right one
whose response takes the company off the internet. A pre-execution probe assesses the blast
radius first, so blocking an address that turns out to be our own VPN egress, or isolating the
jump host the responders log in through, routes to an analyst instead of executing.`,
		"network_detection"},

	{"Security monitoring — a plugin with Go code",
		`An alert matches a brute-force technique, and a sandbox probe runs before the containment
action rather than after it. Against an ordinary workstation the action proceeds. Against a
domain controller the probe refuses, and the circuit breaker downgrades to human approval
instead of isolating a critical asset. Both paths below are the same workflow.`,
		"security_monitoring"},

	{"Expense approval — no Go at all",
		`The entire DAG is a JSON manifest; there is not one line of Go behind it. Under the threshold
it settles immediately. Over it, the run suspends for a manager and hands back a resume pointer;
the decision arrives later and execution continues from where it stopped. The trace spans the
pause as one continuous run, and the pointer cannot be used twice.`,
		"human_approval"},

	{"Three more scenarios",
		`An N-way switch assembled from steps that have only two outbound edges each; scheduling on an
engine that owns no clock, where deferral is a suspension and something else resumes it; and a
batch, where the loop lives in the caller because the DAG rejects cycles outright.`,
		"meta_scenarios"},
}

func (s *site) writeHome(board Board) error {
	packages := readTrim("demo-output/packages.txt")
	tests := readTrim("demo-output/tests.txt")

	var b strings.Builder
	fmt.Fprintf(&b, `<h1>CEE — what actually ran</h1>
<p class="tag">A business-agnostic protocol for deterministic-first execution, where the LLM is an edge tool rather than the driver.</p>

<div class="meta">
Rebuilt on every push to <code>main</code>. This page is generated from a real run on a clean
GitHub runner at commit <code>%s</code> — every block is captured output, none of it is
written by hand. Built %s.
</div>

<ul class="stats">
  <li><b>%s</b><span>packages passing</span></li>
  <li><b>%s</b><span>tests</span></li>
  <li><b>0</b><span>external dependencies</span></li>
</ul>
`, html.EscapeString(s.short), html.EscapeString(s.built),
		html.EscapeString(packages), html.EscapeString(tests))

	for _, sec := range sections {
		fmt.Fprintf(&b, "\n<h2>%s</h2>\n<p>%s</p>\n",
			html.EscapeString(sec.Heading),
			html.EscapeString(collapse(sec.Prose)))
		b.WriteString(capture("demo-output/" + sec.Capture + ".txt"))
	}

	fmt.Fprintf(&b, `
<h2>Plugin catalog</h2>
<p>Plugins are distributed as manifests and ranked by how many model calls they remove, measured
against a baseline agent that would call a model once per step. The
<a href="leaderboard/">full leaderboard</a> has the detail.</p>
`)
	b.WriteString(capture("demo-output/list.txt"))
	b.WriteString(capture("demo-output/bench.txt"))

	b.WriteString("\n<details>\n<summary>Every shipped manifest, statically validated</summary>\n")
	b.WriteString(capture("demo-output/validate.txt"))
	b.WriteString("</details>\n")

	return s.render("index.html", page{
		Title: "CEE — what actually ran",
		Nav:   "home",
		Root:  "",
		Body:  template.HTML(b.String()),
	})
}

func collapse(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func readTrim(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return "—"
	}
	return strings.TrimSpace(string(data))
}

func capture(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Sprintf("<p class=\"empty\">%s was not captured in this run.</p>\n",
			html.EscapeString(filepath.Base(path)))
	}
	return fmt.Sprintf("<pre><code>%s</code></pre>\n",
		html.EscapeString(strings.TrimRight(string(data), "\n")))
}
