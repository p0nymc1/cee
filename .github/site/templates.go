package main

const styles = `
:root { color-scheme: light dark; --fg:#1a1a1a; --mut:#666; --bd:#e3e3e3; --bg:#fff;
        --code:#f6f6f4; --accent:#0b6b5f; --soft:#f0f5f3; --gold:#b8860b; }
@media (prefers-color-scheme: dark) {
  :root { --fg:#e8e8e6; --mut:#9a9a97; --bd:#333; --bg:#151515;
          --code:#1e1e1e; --accent:#5dcaa5; --soft:#182320; --gold:#d7a94b; }
}
* { box-sizing: border-box; }
body { margin:0; background:var(--bg); color:var(--fg);
       font:16px/1.65 ui-sans-serif,system-ui,-apple-system,"Segoe UI",sans-serif; }
main { max-width: 860px; margin: 0 auto; padding: 2rem 1.25rem 5rem; }
h1 { font-size: 1.9rem; font-weight: 600; margin: 0 0 .4rem; letter-spacing: -.01em; }
h2 { font-size: 1.2rem; font-weight: 600; margin: 3rem 0 .5rem; letter-spacing: -.005em; }
h3 { font-size: 1.02rem; font-weight: 600; margin: 2rem 0 .4rem; }
.tag { color: var(--mut); margin: 0 0 2rem; }
nav { border-bottom: 1px solid var(--bd); }
nav div { max-width: 860px; margin: 0 auto; padding: .85rem 1.25rem; display: flex;
          gap: 1.4rem; align-items: baseline; flex-wrap: wrap; }
nav a { color: var(--mut); text-decoration: none; font-size: .9rem; }
nav a:hover { color: var(--fg); }
nav a.here { color: var(--fg); font-weight: 600; }
nav .brand { font-weight: 600; color: var(--fg); letter-spacing: -.01em; margin-right: .4rem; }
.meta { color: var(--mut); font-size: .875rem; border-top: 1px solid var(--bd);
        border-bottom: 1px solid var(--bd); padding: .8rem 0; margin-bottom: 2.5rem; }
.meta code { background: none; padding: 0; }
.stats { display: flex; flex-wrap: wrap; gap: 2.5rem; margin: 0 0 1rem; padding: 0; list-style: none; }
.stats b { display: block; font-size: 1.6rem; font-weight: 600; color: var(--accent); }
.stats span { color: var(--mut); font-size: .85rem; }
p { margin: .5rem 0 1rem; }
pre { background: var(--code); border: 1px solid var(--bd); border-radius: 6px;
      padding: .9rem 1rem; overflow-x: auto; font-size: .8125rem; line-height: 1.5; }
code { font-family: ui-monospace,SFMono-Regular,Menlo,monospace; }
p code, li code, td code { background: var(--code); border-radius: 3px; padding: .1em .35em; font-size: .875em; }
a { color: var(--accent); }
footer { margin-top: 4rem; padding-top: 1.5rem; border-top: 1px solid var(--bd);
         color: var(--mut); font-size: .875rem; }
details { margin: 1rem 0; }
summary { cursor: pointer; color: var(--mut); }
blockquote { margin: 1.2rem 0; padding: .1rem 0 .1rem 1.1rem; border-left: 3px solid var(--accent); color: var(--mut); }
hr { border: 0; border-top: 1px solid var(--bd); margin: 2.5rem 0; }
.scroll { overflow-x: auto; }
table { border-collapse: collapse; width: 100%; font-size: .9rem; }
th, td { text-align: left; padding: .55rem .7rem; border-bottom: 1px solid var(--bd); }
th { font-weight: 600; color: var(--mut); font-size: .8rem; text-transform: uppercase;
     letter-spacing: .04em; white-space: nowrap; }
.board td { vertical-align: top; }
.board .rank { font-weight: 600; color: var(--mut); width: 2.5rem; }
.board .rank.top { color: var(--gold); }
.board .who { min-width: 16rem; }
.board .name { font-weight: 600; white-space: nowrap; }
.board .desc { color: var(--mut); font-size: .85rem; margin-top: .2rem; display: block;
               max-width: 34rem; }
.pill { display: inline-block; font-size: .7rem; letter-spacing: .03em; border: 1px solid var(--bd);
        border-radius: 99px; padding: .05rem .5rem; color: var(--mut); margin-left: .35rem; }
.bar { display: block; height: 5px; border-radius: 3px; background: var(--bd); margin-top: .35rem;
       width: 100%; max-width: 110px; overflow: hidden; }
.bar i { display: block; height: 100%; background: var(--accent); }
.num { font-variant-numeric: tabular-nums; white-space: nowrap; }
.callout { background: var(--soft); border: 1px solid var(--bd); border-radius: 8px;
           padding: 1rem 1.15rem; margin: 1.5rem 0; }
.callout p:last-child { margin-bottom: 0; }
.posts { list-style: none; padding: 0; margin: 2rem 0 0; }
.posts li { padding: 1.15rem 0; border-bottom: 1px solid var(--bd); }
.posts a { text-decoration: none; font-weight: 600; font-size: 1.05rem; color: var(--fg); }
.posts a:hover { color: var(--accent); }
.posts .when { color: var(--mut); font-size: .82rem; margin-top: .2rem; }
.posts p { color: var(--mut); margin: .35rem 0 0; font-size: .93rem; }
article h2 { margin-top: 2.4rem; }
.byline { color: var(--mut); font-size: .875rem; margin: 0 0 2rem; }
.empty { color: var(--mut); font-style: italic; }
`

const shell = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<meta name="description" content="{{.Description}}">
<style>{{.Styles}}</style>
</head>
<body>
<nav><div>
  <span class="brand">CEE</span>
  <a href="{{.Root}}"{{if eq .Nav "home"}} class="here"{{end}}>What ran</a>
  <a href="{{.Root}}leaderboard/"{{if eq .Nav "board"}} class="here"{{end}}>Leaderboard</a>
  <a href="{{.Root}}blog/"{{if eq .Nav "blog"}} class="here"{{end}}>Blog</a>
  <a href="https://github.com/{{.Repo}}">Source</a>
</div></nav>
<main>
{{.Body}}
<footer>
<a href="https://github.com/{{.Repo}}">Source</a> ·
<a href="https://github.com/{{.Repo}}/blob/main/docs/TECHNICAL_SPECIFICATION.md">Specification</a> ·
<a href="https://github.com/{{.Repo}}/blob/main/docs/DEVELOPMENT_GUIDE.md">Development guide</a> ·
<a href="https://github.com/{{.Repo}}/blob/main/docs/NORMATIVE_HANDBOOK.md">Normative handbook</a>
<p>Built {{.Built}} from commit <code>{{.Short}}</code>.</p>
</footer>
</main>
</body>
</html>
`

const boardTemplate = `
<h1>Plugin leaderboard</h1>
<p class="tag">Every plugin in the catalog, ranked by how many model calls it removes.</p>

<div class="meta">
Regenerated by CI from <code>cee bench</code> on a clean runner at commit <code>{{.Short}}</code>.
Nothing on this page is written by hand; a plugin appears here by shipping a benchmark, not by
being described well.
</div>

<ul class="stats">
  <li><b>{{.Board.TotalRemoved}}</b><span>model calls removed</span></li>
  <li><b>{{.Board.TotalEvents}}</b><span>benchmark events</span></li>
  <li><b>{{.Board.TotalErrors}}</b><span>errors</span></li>
  <li><b>{{len .Board.Entries}}</b><span>ranked plugins</span></li>
</ul>

{{if .Board.Entries}}
<div class="scroll">
<table class="board">
<thead><tr>
  <th>#</th><th>Plugin</th><th>Determinism</th><th class="num">Events</th>
  <th class="num">Errors</th><th class="num">Calls removed</th>
</tr></thead>
<tbody>
{{range .Board.Entries}}
<tr>
  <td class="rank{{if eq .Rank 1}} top{{end}}">{{.Rank}}</td>
  <td class="who">
    <span class="name">{{.Plugin}}</span>{{if .Tier}}<span class="pill">{{.Tier}}</span>{{end}}{{if .Version}}<span class="pill">v{{.Version}}</span>{{end}}
    {{if .Description}}<span class="desc">{{.Description}}</span>{{end}}
  </td>
  <td class="num">{{.Determinism}}<span class="bar"><i style="width:{{.Ratio}}%"></i></span></td>
  <td class="num">{{.Events}}</td>
  <td class="num">{{.Errors}}</td>
  <td class="num">{{.Eliminated}}</td>
</tr>
{{end}}
</tbody>
</table>
</div>
{{else}}
<p class="empty">No plugin shipped a benchmark in this run.</p>
{{end}}

{{if .Board.Unranked}}
<h2>In the catalog, not on the board</h2>
<p>These ship a manifest but no <code>benchmark.json</code>, so there is no measurement to rank.
That is a gap in the plugin, not in the plugin's quality — the board only reports what was run.</p>
<div class="scroll">
<table class="board">
<thead><tr><th>Plugin</th><th>Domain</th><th>Tier</th></tr></thead>
<tbody>
{{range .Board.Unranked}}
<tr>
  <td class="who"><span class="name">{{.Plugin}}</span>{{if .Description}}<span class="desc">{{.Description}}</span>{{end}}</td>
  <td>{{.Domain}}</td><td>{{.Tier}}</td>
</tr>
{{end}}
</tbody>
</table>
</div>
{{end}}

<h2>What "calls removed" means</h2>
<p>The baseline is a naive agent that calls a model once per step. Every deterministic step CEE
executes is one call that baseline would have made and CEE did not, so
<code>determinism = deterministic steps / (deterministic steps + extractions)</code> counts calls
that did not happen rather than estimating tokens.</p>

<div class="callout">
<p><strong>What this number is not.</strong> It is not a cost saving in currency, and it is not a
comparison against a real agent implementation. There are no token counts here and no control
group — both are on the roadmap and neither exists yet. A leaderboard that quietly turned call
counts into dollars would be the first thing to collapse under a follow-up question.</p>
</div>

<h2>Getting on this board</h2>
<p>Ship a manifest and a benchmark. No Go required if your process fits the standard action
library:</p>
<pre><code>catalog/plugins/&lt;name&gt;/manifest.json    the workflow
catalog/plugins/&lt;name&gt;/benchmark.json   a set of {workflow_ref, context} events
catalog/index.json                      one entry pointing at both</code></pre>
<p>Then <code>cee lint</code> to check it and <code>cee bench</code> to see where you land. The
full walkthrough is in the
<a href="https://github.com/{{.Repo}}/blob/main/CONTRIBUTING.md">contributing guide</a>.</p>

<details>
<summary>Raw <code>cee bench</code> output</summary>
<pre><code>{{.Board.Raw}}</code></pre>
</details>
`

const blogIndexTemplate = `
<h1>Blog</h1>
<p class="tag">Notes on building a deterministic execution engine — the decisions, and the ones that turned out wrong.</p>

{{if .Posts}}
<ul class="posts">
{{range .Posts}}
<li>
  <a href="{{$.Root}}blog/{{.Slug}}/">{{.Title}}</a>
  <div class="when"><time datetime="{{.ISO}}">{{.Stamp}}</time> · {{.ReadingTime}} min read</div>
  {{if .Summary}}<p>{{.Summary}}</p>{{end}}
</li>
{{end}}
</ul>
{{else}}
<p class="empty">No posts yet.</p>
{{end}}
`

const postTemplate = `
<article>
<h1>{{.Post.Title}}</h1>
<p class="byline"><time datetime="{{.Post.ISO}}">{{.Post.Stamp}}</time> · {{.Post.ReadingTime}} min read{{range .Post.Tags}} · {{.}}{{end}}</p>
{{.Post.Body}}
</article>
<p style="margin-top:3rem"><a href="{{.Root}}blog/">← All posts</a></p>
`
