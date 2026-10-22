// Package report renders plan/apply results. HTML uses html/template
// (auto-escaping — no more hand-rolled html_escape helper), and results
// are also dumped as JSON so future consumers (a Slack notifier, a
// dashboard, `jq`) can build on structured data instead of re-parsing
// HTML or log files.
package report

import (
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"homelab-deploy/internal/graph"
	"homelab-deploy/internal/tf"
)

// Status is the outcome of one directory, independent of whether it came
// from a plan or an apply run.
type Status string

const (
	StatusOK      Status = "OK"      // no changes (plan) or applied cleanly (apply)
	StatusChanges Status = "CHANGES" // plan found pending changes
	StatusError   Status = "ERROR"
)

// Entry is one directory's result, ready to render.
type Entry struct {
	Dir                     string
	Status                  Status
	Detail                  string
	ResourceChanges         []tf.ResourceChange
	Adds, Changes, Destroys int
	RawOutput               string
	// UnsavedPlan is true when this directory's backend refused to save
	// a generated plan locally (see tf.PlanResult.UnsavedPlan) — Detail
	// already carries a human-readable note about this; the field
	// exists separately so a template or JSON consumer can render it
	// distinctly (e.g. a badge) rather than parsing prose.
	UnsavedPlan bool
	// RemotePlanURL links to this run in Terraform Cloud's own UI (see
	// tf.PlanResult.RemotePlanURL) — empty unless UnsavedPlan is also
	// true and TFC's plain-text output actually included the link.
	RemotePlanURL string
	// Warnings carries every warning diagnostic Terraform printed for
	// this directory (see tf.PlanResult.Warnings) — nil for a directory
	// with none, or for a command (init/apply/sync) that doesn't parse
	// them out of its output at all.
	Warnings []tf.Warning
}

// Report is the full run, serializable as-is for JSON consumers. Graph
// is optional (nil/empty is fine — the HTML report's graph tab just
// shows an empty state) since not every caller has a dependency graph
// handy (e.g. a single targeted directory has nothing to graph).
type Report struct {
	GeneratedAt time.Time
	Command     string // "plan" or "apply"
	Entries     []Entry
	Graph       []graph.LayoutNode
}

// FromPlanResult classifies a tf.PlanResult into a report Entry.
func FromPlanResult(pr tf.PlanResult) Entry {
	e := Entry{
		Dir:             pr.Dir,
		ResourceChanges: pr.ResourceChanges,
		Adds:            pr.Adds,
		Changes:         pr.Changes,
		Destroys:        pr.Destroys,
		RawOutput:       pr.RawOutput,
		UnsavedPlan:     pr.UnsavedPlan,
		RemotePlanURL:   pr.RemotePlanURL,
		Warnings:        pr.Warnings,
	}
	switch {
	case pr.Err != nil:
		e.Status = StatusError
		e.Detail = pr.Err.Error()
	case pr.HasChanges && pr.Adds == 0 && pr.Changes == 0 && pr.Destroys == 0:
		// A contradiction: terraform itself says there ARE changes
		// (that's what HasChanges means — it comes from terraform's own
		// exit code, not from anything parsed here), but the counts
		// are all zero. Guards against ever presenting a confidently
		// wrong "0 to add, 0 to change, 0 to destroy" if
		// parsePlanSummaryLine ever fails to match terraform's own
		// summary line for some reason (an unexpected wording change,
		// for instance) — say plainly that the count isn't known
		// instead of trusting a result that contradicts itself.
		e.Status = StatusChanges
		e.Detail = "has changes, but the exact count couldn't be determined for this backend"
		if pr.UnsavedPlan {
			e.Detail += " (plan not saved — this backend doesn't support it; apply will re-plan and show the real changes then)"
		}
	case pr.HasChanges:
		e.Status = StatusChanges
		e.Detail = summaryLine(pr.Adds, pr.Changes, pr.Destroys)
		if pr.UnsavedPlan {
			e.Detail += " (plan not saved — this backend doesn't support it; apply will re-plan)"
		}
	default:
		e.Status = StatusOK
		e.Detail = "no changes"
	}
	return e
}

func summaryLine(adds, changes, destroys int) string {
	return fmt.Sprintf("%d to add, %d to change, %d to destroy", adds, changes, destroys)
}

// severityOrder puts errors first, then changes, then clean dirs — same
// ordering the bash report used, so problems are always visible above
// the fold.
var severityOrder = map[Status]int{StatusError: 0, StatusChanges: 1, StatusOK: 2}

func sortedEntries(entries []Entry) []Entry {
	out := append([]Entry(nil), entries...)
	sort.SliceStable(out, func(i, j int) bool {
		if severityOrder[out[i].Status] != severityOrder[out[j].Status] {
			return severityOrder[out[i].Status] < severityOrder[out[j].Status]
		}
		return out[i].Dir < out[j].Dir
	})
	return out
}

// --- dependency graph SVG ---
//
// Rendered server-side into plain SVG (rect/text/line elements) rather
// than via a client-side graphing library, so the report stays a single
// static file that works with no network access when opened later —
// consistent with everything else in this tool avoiding external
// dependencies at render time.

const (
	svgBoxHeight  = 40
	svgVGap       = 16
	svgHGap       = 70
	svgMargin     = 30
	svgCharWidth  = 7.2
	svgBoxPadding = 28
	svgMinBoxW    = 150
	svgMaxBoxW    = 320
)

// svgNode is one already-positioned box, ready for the template to emit
// directly — all layout math happens here in Go, not in the template.
// Inset* is the manual-step double-border rect, precomputed here rather
// than doing arithmetic in the template (html/template pipelines can't).
// ID and ConnClass wire up the hover-highlight effect — see hoverCSS.
type svgNode struct {
	X, Y, W, H                     int
	InsetX, InsetY, InsetW, InsetH int
	LabelX, LabelY                 int
	Label                          string
	Fill, Stroke                   string
	ManualStep                     bool
	ID                             string // "node-<slug>", the hover target
	ConnClass                      string // "conn-<slug of each neighbor>", space-separated
}

// svgEdge additionally carries ConnClass: "conn-<slug of each endpoint>",
// so the same hover rule that highlights a neighboring node also
// highlights the edge connecting to it.
type svgEdge struct {
	X1, Y1, X2, Y2 int
	ConnClass      string
}

type svgGraph struct {
	Width, Height int
	Nodes         []svgNode
	Edges         []svgEdge
	// HoverCSS is generated CSS text (one rule pair per node with at
	// least one neighbor) using the CSS :has() selector: hovering a node
	// highlights every edge and neighboring node connected to it,
	// without any JavaScript. It's built entirely from cssSlug'd
	// identifiers (guaranteed to contain only [a-zA-Z0-9_-]), so it's
	// safe to mark as template.CSS and skip html/template's CSS
	// auto-escaping, which is tuned for a single embedded value, not a
	// whole generated stylesheet, and would otherwise mangle the
	// selector syntax (colons, parens, #).
	HoverCSS template.CSS
}

// buildGraphSVG lays out nodes left-to-right by dependency Depth (so the
// picture reads the same direction as apply order) and colors each box
// by that run's Entry.Status for the same directory, when there is one —
// a node with no matching entry (graph.Layout was computed but nothing
// was planned/applied for it, e.g. a partial run) falls back to a
// neutral gray rather than guessing a status.
func buildGraphSVG(nodes []graph.LayoutNode, entries []Entry) svgGraph {
	if len(nodes) == 0 {
		return svgGraph{}
	}

	statusByDir := make(map[string]Status, len(entries))
	for _, e := range entries {
		statusByDir[e.Dir] = e.Status
	}

	maxLabelLen, maxDepth := 0, 0
	for _, n := range nodes {
		if len(n.Dir) > maxLabelLen {
			maxLabelLen = len(n.Dir)
		}
		if n.Depth > maxDepth {
			maxDepth = n.Depth
		}
	}
	boxW := int(float64(maxLabelLen)*svgCharWidth) + svgBoxPadding
	if boxW < svgMinBoxW {
		boxW = svgMinBoxW
	}
	if boxW > svgMaxBoxW {
		boxW = svgMaxBoxW
	}

	type point struct{ X, Y int }
	rowInCol := map[int]int{} // depth -> how many nodes placed in that column so far
	pos := make(map[string]point, len(nodes))
	maxRows := 0
	for _, n := range nodes {
		row := rowInCol[n.Depth]
		rowInCol[n.Depth] = row + 1
		if row+1 > maxRows {
			maxRows = row + 1
		}
		pos[n.Dir] = point{
			X: svgMargin + n.Depth*(boxW+svgHGap),
			Y: svgMargin + row*(svgBoxHeight+svgVGap),
		}
	}

	slugOf := slugAll(nodes)

	// neighbors[dir] is every directory connected to dir by an edge, in
	// either direction — used to wire up hover highlighting: hovering a
	// node should highlight both what it depends on AND what depends on
	// it, not just one direction.
	neighbors := make(map[string]map[string]bool, len(nodes))
	addNeighbor := func(a, b string) {
		if neighbors[a] == nil {
			neighbors[a] = map[string]bool{}
		}
		neighbors[a][b] = true
	}
	for _, n := range nodes {
		for _, dep := range n.DependsOn {
			addNeighbor(n.Dir, dep)
			addNeighbor(dep, n.Dir)
		}
	}

	g := svgGraph{
		Width:  svgMargin*2 + (maxDepth+1)*boxW + maxDepth*svgHGap,
		Height: svgMargin*2 + maxRows*svgBoxHeight + max(0, maxRows-1)*svgVGap,
	}

	var hoverCSS strings.Builder
	for _, n := range nodes {
		p := pos[n.Dir]
		fill, stroke := "#f0f0f0", "#999999" // no entry for this dir this run
		switch statusByDir[n.Dir] {
		case StatusError:
			fill, stroke = "#fde3e0", "#b3261e"
		case StatusChanges:
			fill, stroke = "#fff3cd", "#8a6300"
		case StatusOK:
			fill, stroke = "#e6f4ea", "#2e7d32"
		}

		// This node's own box carries "conn-<neighbor>" for each
		// neighbor — i.e. it lights up when ANY of its neighbors is
		// hovered. See the matching rule generation below.
		var connClasses []string
		neighborDirs := sortedKeys(neighbors[n.Dir])
		for _, nb := range neighborDirs {
			connClasses = append(connClasses, "conn-"+slugOf[nb])
		}

		g.Nodes = append(g.Nodes, svgNode{
			X: p.X, Y: p.Y, W: boxW, H: svgBoxHeight,
			InsetX: p.X + 4, InsetY: p.Y + 4, InsetW: boxW - 8, InsetH: svgBoxHeight - 8,
			LabelX: p.X + 10, LabelY: p.Y + svgBoxHeight/2 + 4,
			Label: n.Dir, Fill: fill, Stroke: stroke, ManualStep: n.ManualStep,
			ID:        "node-" + slugOf[n.Dir],
			ConnClass: strings.Join(connClasses, " "),
		})

		if len(neighborDirs) > 0 {
			slug := slugOf[n.Dir]
			fmt.Fprintf(&hoverCSS,
				"svg:has(#node-%s:hover) .conn-%s.graph-node rect.box{stroke:#2563eb;stroke-width:3}\n"+
					"svg:has(#node-%s:hover) line.conn-%s{stroke:#2563eb;stroke-width:3;marker-end:url(#arrow-highlight)}\n",
				slug, slug, slug, slug)
		}
	}

	for _, n := range nodes {
		to := pos[n.Dir]
		for _, dep := range n.DependsOn {
			from, ok := pos[dep]
			if !ok {
				continue
			}
			g.Edges = append(g.Edges, svgEdge{
				X1: from.X + boxW, Y1: from.Y + svgBoxHeight/2,
				X2: to.X, Y2: to.Y + svgBoxHeight/2,
				ConnClass: fmt.Sprintf("conn-%s conn-%s", slugOf[dep], slugOf[n.Dir]),
			})
		}
	}
	g.HoverCSS = template.CSS(hoverCSS.String())
	return g
}

// cssSlug turns a directory path into text safe to use as a CSS
// class/id fragment: only [a-zA-Z0-9_-], never starting with a digit.
// Used solely to wire up hover highlighting — never displayed.
func cssSlug(dir string) string {
	var b strings.Builder
	b.WriteByte('n')
	for _, r := range dir {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}

// slugAll assigns every node a cssSlug, resolving any collision (two
// different Dir values sanitizing to the same slug — vanishingly
// unlikely for real directory names, but handled anyway so the
// generated CSS/ids stay valid and unique regardless) by appending a
// counter.
func slugAll(nodes []graph.LayoutNode) map[string]string {
	slugOf := make(map[string]string, len(nodes))
	used := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		base := cssSlug(n.Dir)
		slug := base
		for i := 2; used[slug]; i++ {
			slug = fmt.Sprintf("%s-%d", base, i)
		}
		used[slug] = true
		slugOf[n.Dir] = slug
	}
	return slugOf
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// WriteHTML renders the report to logDir/report.html.
func WriteHTML(logDir string, rep Report) (string, error) {
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(logDir, "report.html")
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	data := struct {
		Report
		Sorted []Entry
		SVG    svgGraph
	}{
		Report: rep,
		Sorted: sortedEntries(rep.Entries),
		SVG:    buildGraphSVG(rep.Graph, rep.Entries),
	}

	if err := htmlTmpl.Execute(f, data); err != nil {
		return "", err
	}
	return path, nil
}

// WriteJSON renders the report to logDir/report.json — the extensibility
// hook: anything that wants structured results (a notifier, a dashboard,
// a CI gate on `Destroys > 0`) reads this instead of scraping HTML.
func WriteJSON(logDir string, rep Report) (string, error) {
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(logDir, "report.json")
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return path, enc.Encode(rep)
}

// RenderGraph writes a standalone HTML page showing just the dependency
// graph for nodes — no plan/apply results involved, since nodes comes
// straight from graph.Discover+Layout (a filesystem walk of
// .deploy-deps files), not from a plan run. Every box therefore renders
// in the neutral "not run" color from buildGraphSVG's fallback (nil
// entries). This is what lets the live dashboard's /graph route show the
// graph before anyone has ever clicked Plan.
func RenderGraph(w io.Writer, nodes []graph.LayoutNode) error {
	data := struct{ SVG svgGraph }{SVG: buildGraphSVG(nodes, nil)}
	return graphOnlyTmpl.Execute(w, data)
}

// faviconLink is a self-contained SVG favicon (an upward "deploy" arrow
// rising off a launch pad, on a rounded square) embedded as a base64 data
// URI, so it needs no extra static-file route or embed.FS entry.
const faviconLink = `<link rel="icon" type="image/svg+xml" href="data:image/svg+xml;base64,PHN2ZyB4bWxucz0iaHR0cDovL3d3dy53My5vcmcvMjAwMC9zdmciIHZpZXdCb3g9IjAgMCAzMiAzMiI+PHN0eWxlPi5iZ3tmaWxsOiMxYTFhMWF9LmZne2ZpbGw6I2Y3ZjdmOH1AbWVkaWEgKHByZWZlcnMtY29sb3Itc2NoZW1lOmRhcmspey5iZ3tmaWxsOiNlNGU0ZTZ9LmZne2ZpbGw6IzE2MTcxYX19PC9zdHlsZT48cmVjdCBjbGFzcz0iYmciIHdpZHRoPSIzMiIgaGVpZ2h0PSIzMiIgcng9IjciLz48cGF0aCBjbGFzcz0iZmciIGQ9Ik0xNiA1IDI0IDE1IDIwIDE1IDIwIDIzIDEyIDIzIDEyIDE1IDggMTVaIi8+PHJlY3QgY2xhc3M9ImZnIiB4PSI5IiB5PSIyNiIgd2lkdGg9IjE0IiBoZWlnaHQ9IjMiLz48L3N2Zz4K">`

var graphOnlyTmpl = template.Must(template.New("graph").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Dependency graph</title>
` + faviconLink + `
<style>
  body { font-family: -apple-system, Segoe UI, Roboto, sans-serif; margin: 2rem; background: #f7f7f8; color: #1a1a1a; }
  h1 { font-size: 1.4rem; margin-bottom: 0.2rem; }
  a { color: #2563eb; }
  .meta { color: #666; font-size: 0.85rem; margin-bottom: 1.5rem; }
  .graph-legend { display: flex; gap: 1.2rem; flex-wrap: wrap; margin-bottom: 1rem; font-size: 0.82rem; color: #555; }
  .graph-legend .swatch { display: inline-block; width: 0.7rem; height: 0.7rem; border-radius: 2px; margin-right: 0.35rem; vertical-align: -1px; border: 1px solid rgba(0,0,0,0.15); }
  .graph-scroll { overflow: auto; background: #fff; box-shadow: 0 1px 3px rgba(0,0,0,0.08); border-radius: 4px; padding: 0.5rem; }
  .graph-node rect.box { rx: 4; cursor: pointer; transition: stroke 0.12s ease, stroke-width 0.12s ease; }
  .graph-node text { font-family: ui-monospace, monospace; font-size: 11px; fill: #1a1a1a; pointer-events: none; }
  .graph-empty { color: #666; font-size: 0.9rem; padding: 1rem 0; }
  .graph-node:hover rect.box { stroke: #2563eb; stroke-width: 3; }
  .graph-edge { transition: stroke 0.12s ease, stroke-width 0.12s ease; }
</style>
</head>
<body>
<h1>Dependency graph</h1>
<div class="meta"><a href="/">&larr; dashboard</a></div>

{{if .SVG.Nodes}}
<div class="graph-legend">
  <span><span class="swatch" style="background:#f0f0f0;border-color:#999"></span>not run yet</span>
  <span><span class="swatch" style="background:#e6f4ea;border-color:#2e7d32"></span>no changes</span>
  <span><span class="swatch" style="background:#fff3cd;border-color:#8a6300"></span>changes</span>
  <span><span class="swatch" style="background:#fde3e0;border-color:#b3261e"></span>error</span>
  <span>double border = manual step required first</span>
  <span>hover a box to highlight what it links to</span>
</div>
<div class="graph-scroll">
<svg width="{{.SVG.Width}}" height="{{.SVG.Height}}" viewBox="0 0 {{.SVG.Width}} {{.SVG.Height}}" xmlns="http://www.w3.org/2000/svg">
  <defs>
    <marker id="arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse">
      <path d="M 0 0 L 10 5 L 0 10 z" fill="#999" />
    </marker>
    <marker id="arrow-highlight" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse">
      <path d="M 0 0 L 10 5 L 0 10 z" fill="#2563eb" />
    </marker>
  </defs>
  {{if .SVG.HoverCSS}}<style>{{.SVG.HoverCSS}}</style>{{end}}
  {{range .SVG.Edges}}
  <line class="graph-edge {{.ConnClass}}" x1="{{.X1}}" y1="{{.Y1}}" x2="{{.X2}}" y2="{{.Y2}}" stroke="#999" stroke-width="1.5" marker-end="url(#arrow)" />
  {{end}}
  {{range .SVG.Nodes}}
  <g class="graph-node {{.ConnClass}}" id="{{.ID}}">
    <rect class="box" x="{{.X}}" y="{{.Y}}" width="{{.W}}" height="{{.H}}" fill="{{.Fill}}" stroke="{{.Stroke}}" stroke-width="1.5" />
    {{if .ManualStep}}<rect x="{{.InsetX}}" y="{{.InsetY}}" width="{{.InsetW}}" height="{{.InsetH}}" fill="none" stroke="{{.Stroke}}" stroke-width="1" />{{end}}
    <text x="{{.LabelX}}" y="{{.LabelY}}">{{.Label}}</text>
  </g>
  {{end}}
</svg>
</div>
{{else}}
<div class="graph-empty">No dependency graph available — no deployable directories were discovered.</div>
{{end}}

</body>
</html>
`))

var htmlTmpl = template.Must(template.New("report").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>Terraform {{.Command}} report</title>
` + faviconLink + `
<style>
  body { font-family: -apple-system, Segoe UI, Roboto, sans-serif; margin: 2rem; background: #f7f7f8; color: #1a1a1a; }
  h1 { font-size: 1.4rem; margin-bottom: 0.2rem; }
  .meta { color: #666; font-size: 0.85rem; margin-bottom: 1.5rem; }
  table { border-collapse: collapse; width: 100%; background: #fff; box-shadow: 0 1px 3px rgba(0,0,0,0.08); }
  th, td { text-align: left; padding: 0.6rem 0.9rem; border-bottom: 1px solid #eee; vertical-align: top; }
  th { background: #fafafa; font-size: 0.8rem; text-transform: uppercase; letter-spacing: 0.03em; color: #555; }
  .badge { display: inline-block; padding: 0.1rem 0.5rem; border-radius: 4px; font-size: 0.75rem; font-weight: 600; }
  .badge-CHANGES { background: #fff3cd; color: #8a6300; }
  .badge-ERROR { background: #fde3e0; color: #b3261e; }
  .badge-OK { background: #e6f4ea; color: #2e7d32; }
  .dir { font-family: ui-monospace, monospace; }
  .resources { margin: 0.4rem 0 0; padding-left: 1.1rem; font-size: 0.82rem; }
  .resources li.create { color: #2e7d32; }
  .resources li.update { color: #8a6300; }
  .resources li.replace { color: #8a6300; }
  .resources li.delete { color: #b3261e; }
  .warnings { list-style: none; margin: 0.4rem 0 0; padding-left: 0; font-size: 0.82rem; color: #8a6300; }
  .warnings li { padding: 0.2rem 0; }
  .warning-detail { margin: 0.3rem 0 0.2rem; padding: 0.5rem 0.7rem; background: #fdf6e3; border-left: 3px solid #f3d98a; border-radius: 3px; font-size: 0.78rem; color: #6b5000; white-space: pre-wrap; overflow-wrap: anywhere; font-family: ui-monospace, monospace; }
  .attr-diff { list-style: none; margin: 0.25rem 0 0.4rem; padding-left: 0.9rem; border-left: 2px solid #eee; font-size: 0.8rem; color: #1a1a1a; }
  .attr-diff li { padding: 0.1rem 0; }
  .attr-path { font-family: ui-monospace, monospace; color: #555; }
  .attr-before { color: #b3261e; text-decoration: line-through; text-decoration-color: rgba(179,38,30,0.4); }
  .attr-after { color: #2e7d32; font-weight: 600; }
  .attr-arrow { color: #999; }
  details summary { cursor: pointer; color: #555; font-size: 0.85rem; margin-top: 0.4rem; }
  pre { background: #1e1e1e; color: #ddd; padding: 0.8rem; border-radius: 4px; font-size: 0.8rem; margin-top: 0.5rem; white-space: pre-wrap; overflow-wrap: anywhere; word-break: break-word; }

  /* Tabs: pure CSS, no JavaScript. Hidden radios + label[for] + the
     general-sibling combinator (~) to show/hide the matching panel and
     highlight the active label. Requires the radios, tab bar, and
     panels to all be direct siblings in that order — see the body
     markup below. */
  input.tab-radio { position: absolute; opacity: 0; pointer-events: none; }
  .tab-bar { display: flex; gap: 0.25rem; margin-bottom: 1rem; border-bottom: 2px solid #e2e2e2; }
  .tab-label { padding: 0.5rem 1rem; cursor: pointer; color: #666; font-size: 0.9rem; border-bottom: 2px solid transparent; margin-bottom: -2px; user-select: none; }
  .tab-panel { display: none; }
  #tab-results:checked ~ .tab-bar label[for="tab-results"],
  #tab-graph:checked ~ .tab-bar label[for="tab-graph"] { color: #1a1a1a; border-bottom-color: #1a1a1a; font-weight: 600; }
  #tab-results:checked ~ #panel-results,
  #tab-graph:checked ~ #panel-graph { display: block; }

  .graph-legend { display: flex; gap: 1.2rem; flex-wrap: wrap; margin-bottom: 1rem; font-size: 0.82rem; color: #555; }
  .graph-legend .swatch { display: inline-block; width: 0.7rem; height: 0.7rem; border-radius: 2px; margin-right: 0.35rem; vertical-align: -1px; border: 1px solid rgba(0,0,0,0.15); }
  .graph-scroll { overflow: auto; background: #fff; box-shadow: 0 1px 3px rgba(0,0,0,0.08); border-radius: 4px; padding: 0.5rem; }
  .graph-node rect.box { rx: 4; cursor: pointer; transition: stroke 0.12s ease, stroke-width 0.12s ease; }
  .graph-node text { font-family: ui-monospace, monospace; font-size: 11px; fill: #1a1a1a; pointer-events: none; }
  .graph-empty { color: #666; font-size: 0.9rem; padding: 1rem 0; }
  /* Self-highlight on the hovered node; the per-node rules for its
     neighbors and connecting edges are generated in Go (SVG.HoverCSS)
     since which nodes/edges those are depends on the actual dependency
     graph. Uses the CSS :has() selector — supported in all current
     major browsers; on an older one this simply has no effect; nothing
     else breaks. */
  .graph-node:hover rect.box { stroke: #2563eb; stroke-width: 3; }
  .graph-edge { transition: stroke 0.12s ease, stroke-width 0.12s ease; }
</style>
</head>
<body>
<h1>Terraform {{.Command}} report</h1>
<div class="meta">Generated {{.GeneratedAt.Format "2006-01-02 15:04:05 MST"}} &middot; <a href="report.json">raw JSON</a></div>

<input class="tab-radio" type="radio" id="tab-results" name="report-tabs" checked>
<input class="tab-radio" type="radio" id="tab-graph" name="report-tabs">

<div class="tab-bar">
  <label class="tab-label" for="tab-results">Plan Results</label>
  <label class="tab-label" for="tab-graph">Dependency Graph</label>
</div>

<div class="tab-panel" id="panel-results">
<table>
<tr><th>Status</th><th>Directory</th><th>Detail</th></tr>
{{range .Sorted}}
<tr>
  <td><span class="badge badge-{{.Status}}">{{.Status}}</span></td>
  <td class="dir">{{.Dir}}</td>
  <td>
    {{.Detail}}
    {{if .ResourceChanges}}
    <ul class="resources">
      {{range .ResourceChanges}}<li class="{{.Action}}">{{.Action}}: {{.Address}}
        {{if .Attributes}}
        <ul class="attr-diff">
          {{range .Attributes}}<li><code class="attr-path">{{.Path}}</code>: <span class="attr-before">{{.Before}}</span> <span class="attr-arrow">&rarr;</span> <span class="attr-after">{{.After}}</span></li>
          {{end}}
        </ul>
        {{end}}
      </li>
      {{end}}
    </ul>
    {{end}}
    {{if .Warnings}}
    <ul class="warnings">
      {{range .Warnings}}<li>&#9888; {{.Summary}}{{if .Detail}}<pre class="warning-detail">{{.Detail}}</pre>{{end}}</li>{{end}}
    </ul>
    {{end}}
    {{if .RemotePlanURL}}<div><a href="{{.RemotePlanURL}}" target="_blank" rel="noopener">view this run in Terraform Cloud</a></div>{{end}}
    {{if .RawOutput}}<details><summary>full output</summary><pre>{{.RawOutput}}</pre></details>{{end}}
  </td>
</tr>
{{end}}
</table>
</div>

<div class="tab-panel" id="panel-graph">
{{if .SVG.Nodes}}
<div class="graph-legend">
  <span><span class="swatch" style="background:#e6f4ea;border-color:#2e7d32"></span>no changes</span>
  <span><span class="swatch" style="background:#fff3cd;border-color:#8a6300"></span>changes</span>
  <span><span class="swatch" style="background:#fde3e0;border-color:#b3261e"></span>error</span>
  <span><span class="swatch" style="background:#f0f0f0;border-color:#999"></span>not in this run</span>
  <span>double border = manual step required first</span>
  <span>hover a box to highlight what it links to</span>
</div>
<div class="graph-scroll">
<svg width="{{.SVG.Width}}" height="{{.SVG.Height}}" viewBox="0 0 {{.SVG.Width}} {{.SVG.Height}}" xmlns="http://www.w3.org/2000/svg">
  <defs>
    <marker id="arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse">
      <path d="M 0 0 L 10 5 L 0 10 z" fill="#999" />
    </marker>
    <marker id="arrow-highlight" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse">
      <path d="M 0 0 L 10 5 L 0 10 z" fill="#2563eb" />
    </marker>
  </defs>
  {{if .SVG.HoverCSS}}<style>{{.SVG.HoverCSS}}</style>{{end}}
  {{range .SVG.Edges}}
  <line class="graph-edge {{.ConnClass}}" x1="{{.X1}}" y1="{{.Y1}}" x2="{{.X2}}" y2="{{.Y2}}" stroke="#999" stroke-width="1.5" marker-end="url(#arrow)" />
  {{end}}
  {{range .SVG.Nodes}}
  <g class="graph-node {{.ConnClass}}" id="{{.ID}}">
    <rect class="box" x="{{.X}}" y="{{.Y}}" width="{{.W}}" height="{{.H}}" fill="{{.Fill}}" stroke="{{.Stroke}}" stroke-width="1.5" />
    {{if .ManualStep}}<rect x="{{.InsetX}}" y="{{.InsetY}}" width="{{.InsetW}}" height="{{.InsetH}}" fill="none" stroke="{{.Stroke}}" stroke-width="1" />{{end}}
    <text x="{{.LabelX}}" y="{{.LabelY}}">{{.Label}}</text>
  </g>
  {{end}}
</svg>
</div>
{{else}}
<div class="graph-empty">No dependency graph available for this report.</div>
{{end}}
</div>

</body>
</html>
`))
