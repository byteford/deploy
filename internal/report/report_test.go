package report

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"homelab-deploy/internal/graph"
	"homelab-deploy/internal/tf"
)

func TestFromPlanResult_Classification(t *testing.T) {
	cases := []struct {
		name       string
		pr         tf.PlanResult
		wantStatus Status
	}{
		{"error", tf.PlanResult{Dir: "a", Err: errors.New("boom")}, StatusError},
		{"changes", tf.PlanResult{Dir: "a", HasChanges: true, Adds: 1}, StatusChanges},
		{"no changes", tf.PlanResult{Dir: "a", HasChanges: false}, StatusOK},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := FromPlanResult(c.pr)
			if e.Status != c.wantStatus {
				t.Errorf("status = %s, want %s", e.Status, c.wantStatus)
			}
		})
	}
}

func TestFromPlanResult_UnsavedPlanPropagatesAndAnnotatesDetail(t *testing.T) {
	e := FromPlanResult(tf.PlanResult{
		Dir:         "a",
		HasChanges:  true,
		Adds:        2,
		UnsavedPlan: true,
	})
	if !e.UnsavedPlan {
		t.Error("expected Entry.UnsavedPlan = true")
	}
	if !strings.Contains(e.Detail, "plan not saved") {
		t.Errorf("Detail should note the plan wasn't saved, got: %q", e.Detail)
	}
	if e.Status != StatusChanges {
		t.Errorf("status = %s, want StatusChanges — UnsavedPlan shouldn't change classification", e.Status)
	}
}

func TestFromPlanResult_UnsavedPlanWithNoChangesDoesNotAnnotate(t *testing.T) {
	// No changes means nothing would ever need the saved plan file
	// anyway — the annotation would just be noise.
	e := FromPlanResult(tf.PlanResult{Dir: "a", HasChanges: false, UnsavedPlan: true})
	if strings.Contains(e.Detail, "plan not saved") {
		t.Errorf("Detail shouldn't mention unsaved plan when there are no changes to apply, got: %q", e.Detail)
	}
}

func TestFromPlanResult_WarningsPropagate(t *testing.T) {
	e := FromPlanResult(tf.PlanResult{
		Dir:        "a",
		HasChanges: false,
		Warnings:   []tf.Warning{{Summary: "Argument is deprecated", Detail: "This attribute is deprecated."}},
	})
	if len(e.Warnings) != 1 || e.Warnings[0].Summary != "Argument is deprecated" || e.Warnings[0].Detail != "This attribute is deprecated." {
		t.Errorf("Warnings = %+v, want a single deprecated-argument warning with its detail", e.Warnings)
	}
}

func TestFromPlanResult_ErrorTakesPriorityOverChanges(t *testing.T) {
	// A plan that both errored AND reported HasChanges (shouldn't happen,
	// but defensively) must classify as an error, not changes — errors
	// need to be the most visible thing in the report.
	pr := tf.PlanResult{Dir: "a", HasChanges: true, Err: errors.New("boom")}
	e := FromPlanResult(pr)
	if e.Status != StatusError {
		t.Errorf("status = %s, want %s", e.Status, StatusError)
	}
}

func TestSortedEntries_ErrorsFirstThenChangesThenOK(t *testing.T) {
	entries := []Entry{
		{Dir: "z-ok", Status: StatusOK},
		{Dir: "b-changes", Status: StatusChanges},
		{Dir: "a-error", Status: StatusError},
		{Dir: "a-changes", Status: StatusChanges},
	}
	got := sortedEntries(entries)
	want := []string{"a-error", "a-changes", "b-changes", "z-ok"}
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d", len(got), len(want))
	}
	for i, dir := range want {
		if got[i].Dir != dir {
			t.Errorf("position %d = %s, want %s (full order: %v)", i, got[i].Dir, dir, dirsOf(got))
		}
	}
}

func dirsOf(entries []Entry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Dir
	}
	return out
}

func TestWriteHTML_UnsavedPlanAnnotationRendersInRealOutput(t *testing.T) {
	// End-to-end through the real pipeline (FromPlanResult -> WriteHTML)
	// rather than hand-constructing an Entry with the annotation already
	// in Detail — confirms the actual html/template execution renders
	// it, not just that the template *references* .Detail. Includes a
	// real ResourceChanges entry too, since planWithoutSavedFile now
	// gets full per-resource data via PlanJSON, not just totals — the
	// report should show the same resource-level detail for this
	// directory as any other, not a degraded view.
	dir := t.TempDir()
	entry := FromPlanResult(tf.PlanResult{
		Dir:             "services/tfc-managed",
		HasChanges:      true,
		Adds:            1,
		UnsavedPlan:     true,
		ResourceChanges: []tf.ResourceChange{{Address: "terraform_data.example", Action: "create"}},
	})
	rep := Report{Command: "plan", Entries: []Entry{entry}}

	path, err := WriteHTML(dir, rep)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	html := string(data)

	for _, want := range []string{"services/tfc-managed", "plan not saved", "terraform_data.example"} {
		if !strings.Contains(html, want) {
			t.Errorf("HTML report missing %q", want)
		}
	}
}

func TestWriteHTML_ContainsExpectedContent(t *testing.T) {
	dir := t.TempDir()
	rep := Report{
		Command: "plan",
		Entries: []Entry{
			{Dir: "services/palworld", Status: StatusChanges, Detail: "1 to add, 0 to change, 0 to destroy",
				ResourceChanges: []tf.ResourceChange{{Address: "nomad_job.palworld", Action: "create"}}},
			{Dir: "config/vault", Status: StatusError, Detail: "plan failed"},
		},
	}

	path, err := WriteHTML(dir, rep)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	html := string(data)

	for _, want := range []string{"services/palworld", "config/vault", "nomad_job.palworld", "badge-CHANGES", "badge-ERROR"} {
		if !strings.Contains(html, want) {
			t.Errorf("HTML report missing %q", want)
		}
	}
}

func TestWriteHTML_EscapesUntrustedContent(t *testing.T) {
	// Detail/RawOutput can contain terraform's own text output, which in
	// principle could include characters that matter to HTML. html/template
	// auto-escapes; this pins that down instead of trusting it silently.
	dir := t.TempDir()
	rep := Report{
		Command: "plan",
		Entries: []Entry{
			{Dir: "services/a", Status: StatusError, Detail: `<script>alert(1)</script>`},
		},
	}
	path, err := WriteHTML(dir, rep)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "<script>alert(1)</script>") {
		t.Error("unescaped <script> tag made it into the HTML report")
	}
}

func TestWriteHTML_RendersWarnings(t *testing.T) {
	dir := t.TempDir()
	rep := Report{
		Command: "plan",
		Entries: []Entry{
			{Dir: "services/a", Status: StatusChanges, Detail: "1 to add",
				Warnings: []tf.Warning{
					{Summary: "Argument is deprecated", Detail: "line 1 of detail\nline 2 of detail"},
					{Summary: `<script>alert(1)</script>`, Detail: `<script>alert(2)</script>`},
				}},
			{Dir: "services/b", Status: StatusOK, Detail: "no changes"},
		},
	}
	path, err := WriteHTML(dir, rep)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	html := string(data)
	if !strings.Contains(html, "Argument is deprecated") {
		t.Error("HTML report missing the warning summary text")
	}
	if !strings.Contains(html, "line 1 of detail") || !strings.Contains(html, "line 2 of detail") {
		t.Error("HTML report missing the multi-line warning detail")
	}
	if !strings.Contains(html, `class="warnings"`) || !strings.Contains(html, `class="warning-detail"`) {
		t.Error("HTML report missing the warnings/warning-detail markup")
	}
	if strings.Contains(html, "<script>alert(1)</script>") || strings.Contains(html, "<script>alert(2)</script>") {
		t.Error("unescaped <script> tag made it into a rendered warning")
	}
}

func TestWriteJSON_RoundTrips(t *testing.T) {
	dir := t.TempDir()
	rep := Report{
		Command: "apply",
		Entries: []Entry{
			{Dir: "services/a", Status: StatusOK, Detail: "applied"},
		},
	}
	path, err := WriteJSON(dir, rep)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "report.json" {
		t.Errorf("unexpected report path: %s", path)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got Report
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Command != "apply" || len(got.Entries) != 1 || got.Entries[0].Dir != "services/a" {
		t.Errorf("round-tripped report doesn't match: %+v", got)
	}
}

func TestBuildGraphSVG_ColorsByStatus(t *testing.T) {
	nodes := []graph.LayoutNode{
		{Dir: "services/a", Depth: 0},
		{Dir: "services/b", Depth: 1, DependsOn: []string{"services/a"}},
		{Dir: "services/c", Depth: 0},
	}
	entries := []Entry{
		{Dir: "services/a", Status: StatusOK},
		{Dir: "services/b", Status: StatusError},
		// services/c intentionally has no entry -> should fall back to neutral gray
	}

	svg := buildGraphSVG(nodes, entries)
	if len(svg.Nodes) != 3 {
		t.Fatalf("got %d nodes, want 3", len(svg.Nodes))
	}

	byLabel := map[string]svgNode{}
	for _, n := range svg.Nodes {
		byLabel[n.Label] = n
	}

	if got := byLabel["services/a"].Fill; got != "#e6f4ea" {
		t.Errorf("services/a fill = %q, want the OK color", got)
	}
	if got := byLabel["services/b"].Fill; got != "#fde3e0" {
		t.Errorf("services/b fill = %q, want the ERROR color", got)
	}
	if got := byLabel["services/c"].Fill; got != "#f0f0f0" {
		t.Errorf("services/c (no entry) fill = %q, want the neutral color", got)
	}
}

func TestBuildGraphSVG_ManualStepFlagPreserved(t *testing.T) {
	nodes := []graph.LayoutNode{
		{Dir: "services/a", Depth: 0, ManualStep: true},
		{Dir: "services/b", Depth: 0, ManualStep: false},
	}
	svg := buildGraphSVG(nodes, nil)
	byLabel := map[string]svgNode{}
	for _, n := range svg.Nodes {
		byLabel[n.Label] = n
	}
	if !byLabel["services/a"].ManualStep {
		t.Error("services/a should carry ManualStep=true through to the svgNode")
	}
	if byLabel["services/b"].ManualStep {
		t.Error("services/b should carry ManualStep=false through to the svgNode")
	}
}

func TestBuildGraphSVG_EdgesConnectDependencies(t *testing.T) {
	nodes := []graph.LayoutNode{
		{Dir: "config/vault", Depth: 0},
		{Dir: "services/a", Depth: 1, DependsOn: []string{"config/vault"}},
	}
	svg := buildGraphSVG(nodes, nil)
	if len(svg.Edges) != 1 {
		t.Fatalf("got %d edges, want 1", len(svg.Edges))
	}
	edge := svg.Edges[0]
	// The edge must run from vault's right edge to services/a's left
	// edge, at the same vertical midpoint on both ends (both are in
	// single-node columns here, so same row).
	if edge.Y1 != edge.Y2 {
		t.Errorf("edge Y1=%d Y2=%d, want equal (same row)", edge.Y1, edge.Y2)
	}
	if edge.X1 >= edge.X2 {
		t.Errorf("edge X1=%d should be left of X2=%d (dependency -> dependent, left to right)", edge.X1, edge.X2)
	}
}

func TestBuildGraphSVG_EmptyInputProducesEmptyGraph(t *testing.T) {
	svg := buildGraphSVG(nil, nil)
	if len(svg.Nodes) != 0 || len(svg.Edges) != 0 {
		t.Errorf("expected an empty graph for no nodes, got %+v", svg)
	}
}

func TestWriteHTML_IncludesGraphTab(t *testing.T) {
	dir := t.TempDir()
	rep := Report{
		Command: "plan",
		Entries: []Entry{
			{Dir: "services/a", Status: StatusChanges, Detail: "1 to add"},
		},
		Graph: []graph.LayoutNode{
			{Dir: "services/a", Depth: 0},
		},
	}
	path, err := WriteHTML(dir, rep)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	html := string(data)

	for _, want := range []string{"Dependency Graph", "Plan Results", "<svg", "services/a"} {
		if !strings.Contains(html, want) {
			t.Errorf("HTML report missing %q for the graph tab", want)
		}
	}
}

func TestWriteHTML_GraphTabEmptyState(t *testing.T) {
	dir := t.TempDir()
	rep := Report{Command: "plan"} // no Graph at all
	path, err := WriteHTML(dir, rep)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	html := string(data)
	if !strings.Contains(html, "No dependency graph available") {
		t.Error("expected the empty-state message when Report.Graph is empty")
	}
	if strings.Contains(html, "<svg") {
		t.Error("should not render an <svg> element when there's nothing to graph")
	}
}

func TestBuildGraphSVG_HoverWiring_NodeIDsAndClasses(t *testing.T) {
	// vault <- a (a depends on vault). Hovering vault must highlight a
	// (and the edge), and hovering a must highlight vault (and the edge)
	// — i.e. the relationship is wired in both directions, not just
	// "what I depend on".
	nodes := []graph.LayoutNode{
		{Dir: "config/vault", Depth: 0},
		{Dir: "services/a", Depth: 1, DependsOn: []string{"config/vault"}},
	}
	svg := buildGraphSVG(nodes, nil)

	byLabel := map[string]svgNode{}
	for _, n := range svg.Nodes {
		byLabel[n.Label] = n
	}
	vault, a := byLabel["config/vault"], byLabel["services/a"]

	if vault.ID == "" || a.ID == "" || vault.ID == a.ID {
		t.Fatalf("expected distinct non-empty node IDs, got vault=%q a=%q", vault.ID, a.ID)
	}

	// a's box must carry a class that matches when vault is hovered:
	// "conn-<vault's slug>".
	vaultSlug := strings.TrimPrefix(vault.ID, "node-")
	if !strings.Contains(a.ConnClass, "conn-"+vaultSlug) {
		t.Errorf("services/a.ConnClass = %q, want it to contain conn-%s (so hovering vault highlights it)", a.ConnClass, vaultSlug)
	}

	// And vice versa: vault's box must carry a class matching when a is
	// hovered.
	aSlug := strings.TrimPrefix(a.ID, "node-")
	if !strings.Contains(vault.ConnClass, "conn-"+aSlug) {
		t.Errorf("config/vault.ConnClass = %q, want it to contain conn-%s (so hovering services/a highlights it)", vault.ConnClass, aSlug)
	}

	// The edge between them must carry classes for BOTH endpoints, so it
	// lights up regardless of which end is hovered.
	if len(svg.Edges) != 1 {
		t.Fatalf("got %d edges, want 1", len(svg.Edges))
	}
	edge := svg.Edges[0]
	if !strings.Contains(edge.ConnClass, "conn-"+vaultSlug) || !strings.Contains(edge.ConnClass, "conn-"+aSlug) {
		t.Errorf("edge.ConnClass = %q, want it to contain both conn-%s and conn-%s", edge.ConnClass, vaultSlug, aSlug)
	}

	// And the generated CSS must actually reference both node IDs as
	// :has() hover targets.
	css := string(svg.HoverCSS)
	if !strings.Contains(css, "#"+vault.ID+":hover") {
		t.Errorf("HoverCSS missing a rule keyed on #%s:hover:\n%s", vault.ID, css)
	}
	if !strings.Contains(css, "#"+a.ID+":hover") {
		t.Errorf("HoverCSS missing a rule keyed on #%s:hover:\n%s", a.ID, css)
	}
}

func TestBuildGraphSVG_IsolatedNodeGetsNoHoverRule(t *testing.T) {
	// A node with no dependencies and nothing depending on it has
	// nothing to highlight, so it shouldn't get a (pointless) :has()
	// rule generated for it.
	nodes := []graph.LayoutNode{
		{Dir: "services/lonely", Depth: 0},
	}
	svg := buildGraphSVG(nodes, nil)
	if svg.Nodes[0].ConnClass != "" {
		t.Errorf("isolated node ConnClass = %q, want empty", svg.Nodes[0].ConnClass)
	}
	if strings.Contains(string(svg.HoverCSS), svg.Nodes[0].ID) {
		t.Errorf("HoverCSS should not reference an isolated node's ID:\n%s", svg.HoverCSS)
	}
}

func TestSlugAll_HandlesCollisions(t *testing.T) {
	// Two different Dir values that happen to sanitize to the same base
	// slug (e.g. differing only by characters cssSlug maps to the same
	// "-") must still end up with distinct slugs.
	nodes := []graph.LayoutNode{
		{Dir: "services/a-b", Depth: 0},
		{Dir: "services/a.b", Depth: 0}, // "." also sanitizes to "-"
	}
	slugs := slugAll(nodes)
	s1, s2 := slugs["services/a-b"], slugs["services/a.b"]
	if s1 == "" || s2 == "" {
		t.Fatalf("expected non-empty slugs, got %q and %q", s1, s2)
	}
	if s1 == s2 {
		t.Errorf("expected distinct slugs for colliding input, got the same slug %q for both", s1)
	}
}

func TestWriteHTML_HoverCSSEmbedded(t *testing.T) {
	dir := t.TempDir()
	rep := Report{
		Command: "plan",
		Graph: []graph.LayoutNode{
			{Dir: "config/vault", Depth: 0},
			{Dir: "services/a", Depth: 1, DependsOn: []string{"config/vault"}},
		},
	}
	path, err := WriteHTML(dir, rep)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	html := string(data)
	if !strings.Contains(html, ":has(") {
		t.Error("expected the generated :has()-based hover CSS to be embedded in the report")
	}
	if !strings.Contains(html, "arrow-highlight") {
		t.Error("expected the highlighted arrowhead marker to be defined")
	}
}

func TestFromPlanResult_HasChangesButZeroCountsIsHonestNotZero(t *testing.T) {
	// A real, observed contradiction: HasChanges comes from terraform's
	// own exit code, not from anything parsed — if a directory's
	// backend makes that parsing unreliable (a remotely-executed plan
	// via Terraform's "remote" backend proxies a run through Terraform
	// Cloud's own infrastructure, whose JSON output apparently doesn't
	// match what this tool's parser expects), presenting a confident
	// "0 to add, 0 to change, 0 to destroy" would be actively wrong,
	// not just incomplete.
	e := FromPlanResult(tf.PlanResult{
		Dir:         "a",
		HasChanges:  true,
		UnsavedPlan: true,
		// Adds/Changes/Destroys left at their zero value deliberately.
	})
	if e.Status != StatusChanges {
		t.Errorf("status = %s, want StatusChanges — there ARE changes, just an unknown count", e.Status)
	}
	if strings.Contains(e.Detail, "0 to add") {
		t.Errorf("Detail should never claim a specific zero count here, got: %q", e.Detail)
	}
	if !strings.Contains(e.Detail, "couldn't be determined") {
		t.Errorf("Detail should say plainly the count is unknown, got: %q", e.Detail)
	}
}

func TestFromPlanResult_RealCountsOfZeroChangesStillShowsNormally(t *testing.T) {
	// Guard against over-correcting: a directory that genuinely has no
	// changes at all (HasChanges = false) must still show the normal
	// "no changes" status — the contradiction check is specifically
	// about HasChanges=true with zero counts, not about zero counts on
	// their own.
	e := FromPlanResult(tf.PlanResult{Dir: "a", HasChanges: false})
	if e.Status != StatusOK || e.Detail != "no changes" {
		t.Errorf("got status=%s detail=%q, want StatusOK/\"no changes\"", e.Status, e.Detail)
	}
}
