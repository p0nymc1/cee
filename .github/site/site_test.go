package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMarkdownEscapesTextItDidNotAuthor(t *testing.T) {
	got := renderMarkdown("A <script>alert(1)</script> & an ampersand.")
	if strings.Contains(got, "<script>") {
		t.Fatalf("raw script tag survived: %q", got)
	}
	for _, want := range []string{"&lt;script&gt;", "&amp;"} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %q in %q", want, got)
		}
	}
}

func TestMarkdownEscapesInsideCodeSpansAndFences(t *testing.T) {
	span := renderMarkdown("try `<b>bold</b>` here")
	if !strings.Contains(span, "<code>&lt;b&gt;bold&lt;/b&gt;</code>") {
		t.Errorf("code span not escaped: %q", span)
	}

	fence := renderMarkdown("```\n<img src=x onerror=y>\n```")
	if strings.Contains(fence, "<img") {
		t.Errorf("fenced block not escaped: %q", fence)
	}
}

func TestMarkdownRendersTheConstructsPostsUse(t *testing.T) {
	src := `# Title

Some **bold** and *italic* and a [link](https://example.com).

- one
- two

1. first
2. second

> a quote

| a | b |
|---|---|
| 1 | 2 |

` + "```go\nfunc main() {}\n```"

	got := renderMarkdown(src)
	for _, want := range []string{
		"<h1>Title</h1>",
		"<strong>bold</strong>",
		"<em>italic</em>",
		`<a href="https://example.com">link</a>`,
		"<ul>", "<li>one</li>",
		"<ol>", "<li>first</li>",
		"<blockquote><p>a quote</p></blockquote>",
		"<thead>", "<th>a</th>", "<td>1</td>",
		`<code class="lang-go">`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestMarkdownDoesNotTurnAnUnderscoredIdentifierIntoEmphasis(t *testing.T) {
	got := renderMarkdown("the field is cee.failure_reason and cee.failed_step")
	if strings.Contains(got, "<em>") {
		t.Errorf("snake_case became emphasis: %q", got)
	}
}

func TestPostFrontMatterIsParsed(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "hello.md"), `---
title: Hello
date: 2026-07-31
summary: A summary.
tags: one, two
---

Body text.
`)

	posts, err := loadPosts(dir)
	if err != nil {
		t.Fatalf("loadPosts: %v", err)
	}
	if len(posts) != 1 {
		t.Fatalf("got %d posts, want 1", len(posts))
	}

	p := posts[0]
	if p.Title != "Hello" || p.Slug != "hello" || p.Summary != "A summary." {
		t.Errorf("front matter wrong: %+v", p)
	}
	if p.ISO() != "2026-07-31" {
		t.Errorf("ISO = %q", p.ISO())
	}
	if len(p.Tags) != 2 || p.Tags[0] != "one" || p.Tags[1] != "two" {
		t.Errorf("tags = %v", p.Tags)
	}
	if !strings.Contains(string(p.Body), "Body text.") {
		t.Errorf("body missing: %q", p.Body)
	}
}

func TestAPostMissingItsDateIsRejectedRatherThanDatedToday(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "bad.md"), "---\ntitle: No date\n---\n\nBody.\n")

	if _, err := loadPosts(dir); err == nil {
		t.Fatal("a post with no date must fail the build, not silently publish")
	}
}

func TestPostsAreNewestFirst(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "old.md"), "---\ntitle: Old\ndate: 2026-01-01\n---\n\nx\n")
	write(t, filepath.Join(dir, "new.md"), "---\ntitle: New\ndate: 2026-07-31\n---\n\nx\n")

	posts, err := loadPosts(dir)
	if err != nil {
		t.Fatalf("loadPosts: %v", err)
	}
	if posts[0].Title != "New" {
		t.Errorf("order = %s then %s, want newest first", posts[0].Title, posts[1].Title)
	}
}

func TestBoardParsesBenchOutputAndEnrichesFromTheCatalog(t *testing.T) {
	dir := t.TempDir()
	bench := filepath.Join(dir, "bench.txt")
	index := filepath.Join(dir, "index.json")

	write(t, bench, `rank plugin           determinism  events     errors   LLM calls eliminated vs agent
1    access-review    100%         4          0        8 of 8
2    sla-guard        75%          4          1        6 of 8
`)
	write(t, index, `{"plugins":[
  {"name":"access-review","description":"Flags an account.","version":"0.1.0","tier":"L1","domain":"access-review"},
  {"name":"sla-guard","description":"Marks a ticket.","version":"0.1.0","tier":"L1","domain":"sla-guard"},
  {"name":"no-benchmark","description":"Never ran.","version":"0.1.0","tier":"L1","domain":"nb"}
]}`)

	board, err := loadBoard(bench, index)
	if err != nil {
		t.Fatalf("loadBoard: %v", err)
	}

	if len(board.Entries) != 2 {
		t.Fatalf("got %d ranked entries, want 2", len(board.Entries))
	}
	first := board.Entries[0]
	if first.Plugin != "access-review" || first.Rank != 1 {
		t.Errorf("first entry = %+v", first)
	}
	if first.Description != "Flags an account." || first.Tier != "L1" {
		t.Errorf("catalog metadata not joined: %+v", first)
	}
	if first.Ratio != 100 || first.Removed != 8 || first.Baseline != 8 {
		t.Errorf("parsed numbers wrong: ratio=%v removed=%d baseline=%d",
			first.Ratio, first.Removed, first.Baseline)
	}

	if board.TotalRemoved != 14 || board.TotalEvents != 8 || board.TotalErrors != 1 {
		t.Errorf("totals wrong: removed=%d events=%d errors=%d",
			board.TotalRemoved, board.TotalEvents, board.TotalErrors)
	}

	if len(board.Unranked) != 1 || board.Unranked[0].Plugin != "no-benchmark" {
		t.Errorf("a catalog plugin with no benchmark must still be listed, got %+v", board.Unranked)
	}
}

func TestBoardSurvivesAnEmptyBench(t *testing.T) {
	dir := t.TempDir()
	bench := filepath.Join(dir, "bench.txt")
	write(t, bench, "rank plugin determinism events errors LLM calls eliminated vs agent\n")

	board, err := loadBoard(bench, filepath.Join(dir, "missing.json"))
	if err != nil {
		t.Fatalf("a run with no ranked plugins is not an error: %v", err)
	}
	if len(board.Entries) != 0 {
		t.Errorf("got %d entries from a header-only file", len(board.Entries))
	}
}

func TestGeneratedSiteHasEveryPageAndNoUnescapedCapture(t *testing.T) {
	root := t.TempDir()
	mustChdir(t, root)

	write(t, filepath.Join(root, "demo-output", "bench.txt"),
		"rank plugin determinism events errors LLM calls eliminated vs agent\n1    p    100%    1    0    2 of 2\n")
	write(t, filepath.Join(root, "demo-output", "packages.txt"), "26\n")
	write(t, filepath.Join(root, "demo-output", "tests.txt"), "251\n")
	write(t, filepath.Join(root, "demo-output", "rule_change.txt"), "trace: a -> b & <injected>\n")
	write(t, filepath.Join(root, "catalog", "index.json"), `{"plugins":[]}`)
	write(t, filepath.Join(root, "blog", "p.md"), "---\ntitle: P\ndate: 2026-07-31\n---\n\nBody.\n")

	t.Setenv("SITE_OUT", filepath.Join(root, "site"))
	if err := run(); err != nil {
		t.Fatalf("run: %v", err)
	}

	for _, path := range []string{
		"site/index.html",
		"site/leaderboard/index.html",
		"site/blog/index.html",
		"site/blog/p/index.html",
	} {
		if _, err := os.Stat(filepath.Join(root, path)); err != nil {
			t.Errorf("missing %s: %v", path, err)
		}
	}

	home := read(t, filepath.Join(root, "site/index.html"))
	if strings.Contains(home, "<injected>") {
		t.Error("captured output was not escaped; a trace containing markup could inject into the page")
	}
	if !strings.Contains(home, "&lt;injected&gt;") {
		t.Error("captured output is missing entirely")
	}
	for _, want := range []string{`href="leaderboard/"`, `href="blog/"`} {
		if !strings.Contains(home, want) {
			t.Errorf("home page does not link to %q", want)
		}
	}
}

func TestAMissingCaptureIsReportedRatherThanSilentlyBlank(t *testing.T) {
	got := capture("demo-output/definitely-not-here.txt")
	if !strings.Contains(got, "not captured") {
		t.Errorf("a missing capture should say so, got %q", got)
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func mustChdir(t *testing.T, dir string) {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(old) })
}
