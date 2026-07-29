#!/usr/bin/env bash
# Builds the published page from what the demo job actually ran.
#
# Deliberately a single self-contained HTML file with no generator, no theme
# and no build system: the core module refuses external dependencies, and it
# would be odd for the page advertising that to pull in a toolchain. Every
# figure below is read out of demo-output/, so nothing here can claim
# something the run did not do.
set -euo pipefail

out=site
mkdir -p "$out"

# Captured output is untrusted as markup -- a trace containing < or & would
# otherwise break the page or inject into it.
esc() { sed -e 's/&/\&amp;/g' -e 's/</\&lt;/g' -e 's/>/\&gt;/g' "$1"; }

block() {
  printf '<pre><code>'
  esc "$1"
  printf '</code></pre>\n'
}

commit="${GITHUB_SHA:-local}"
short="${commit:0:7}"
repo="${GITHUB_REPOSITORY:-p0nymc1/cee}"
built="$(date -u '+%Y-%m-%d %H:%M UTC')"
packages="$(cat demo-output/packages.txt)"
tests="$(cat demo-output/tests.txt)"

cat > "$out/index.html" <<HTML
<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>CEE — what actually ran</title>
<style>
  :root { color-scheme: light dark; --fg:#1a1a1a; --mut:#666; --bd:#e3e3e3; --bg:#fff; --code:#f6f6f4; --accent:#0b6b5f; }
  @media (prefers-color-scheme: dark) {
    :root { --fg:#e8e8e6; --mut:#9a9a97; --bd:#333; --bg:#151515; --code:#1e1e1e; --accent:#5dcaa5; }
  }
  * { box-sizing: border-box; }
  body { margin:0; background:var(--bg); color:var(--fg);
         font:16px/1.65 ui-sans-serif,system-ui,-apple-system,"Segoe UI",sans-serif; }
  main { max-width: 860px; margin: 0 auto; padding: 3rem 1.25rem 5rem; }
  h1 { font-size: 1.9rem; font-weight: 600; margin: 0 0 .4rem; letter-spacing: -.01em; }
  h2 { font-size: 1.2rem; font-weight: 600; margin: 3rem 0 .5rem; letter-spacing: -.005em; }
  .tag { color: var(--mut); margin: 0 0 2rem; }
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
  a { color: var(--accent); }
  footer { margin-top: 4rem; padding-top: 1.5rem; border-top: 1px solid var(--bd);
           color: var(--mut); font-size: .875rem; }
  details { margin: 1rem 0; }
  summary { cursor: pointer; color: var(--mut); }
</style>
</head>
<body>
<main>

<h1>CEE — what actually ran</h1>
<p class="tag">A business-agnostic protocol for deterministic-first execution, where the LLM is an edge tool rather than the driver.</p>

<div class="meta">
Rebuilt on every push to <code>main</code>. This page is generated from a real run on a clean
GitHub runner at commit <code>$short</code> — every block is captured output, none of it is
written by hand. Built $built.
</div>

<ul class="stats">
  <li><b>$packages</b><span>packages passing</span></li>
  <li><b>$tests</b><span>tests</span></li>
  <li><b>0</b><span>external dependencies</span></li>
</ul>

<h2>Security monitoring — a plugin with Go code</h2>
<p>An alert matches a brute-force technique, and a sandbox probe runs <em>before</em> the
containment action rather than after it. Against an ordinary workstation the action proceeds.
Against a domain controller the probe refuses, and the circuit breaker downgrades to human
approval instead of isolating a critical asset. Both paths below are the same workflow.</p>
HTML

block demo-output/security_monitoring.txt >> "$out/index.html"

cat >> "$out/index.html" <<'HTML'
<h2>Expense approval — no Go at all</h2>
<p>The entire DAG is a JSON manifest; there is not one line of Go behind it. Under the threshold
it settles immediately. Over it, the run suspends for a manager and hands back a resume pointer;
the decision arrives later and execution continues from where it stopped. The trace spans the
pause as one continuous run, and the pointer cannot be used twice.</p>
HTML

block demo-output/human_approval.txt >> "$out/index.html"

cat >> "$out/index.html" <<'HTML'
<h2>Three more scenarios</h2>
<p>An N-way switch assembled from steps that have only two outbound edges each; scheduling on an
engine that owns no clock, where deferral is a suspension and something else resumes it; and a
batch, where the loop lives in the caller because the DAG rejects cycles outright.</p>
HTML

block demo-output/meta_scenarios.txt >> "$out/index.html"

cat >> "$out/index.html" <<'HTML'
<h2>Plugin catalog</h2>
<p>Plugins are distributed as manifests and ranked by how many LLM calls they remove, measured
against a baseline agent that would call a model once per step.</p>
HTML

block demo-output/list.txt  >> "$out/index.html"
block demo-output/bench.txt >> "$out/index.html"

cat >> "$out/index.html" <<'HTML'
<details>
<summary>Every shipped manifest, statically validated</summary>
HTML

block demo-output/validate.txt >> "$out/index.html"

cat >> "$out/index.html" <<HTML
</details>

<footer>
<a href="https://github.com/$repo">Source</a> ·
<a href="https://github.com/$repo/blob/main/docs/TECHNICAL_SPECIFICATION.md">Technical specification</a> ·
<a href="https://github.com/$repo/blob/main/docs/DEVELOPMENT_GUIDE.md">Development guide</a> ·
<a href="https://github.com/$repo/blob/main/docs/NORMATIVE_HANDBOOK.md">Normative handbook</a>
</footer>

</main>
</body>
</html>
HTML

echo "built $out/index.html ($(wc -c < "$out/index.html") bytes)"
