package server

import "html/template"

// faviconLink is a self-contained SVG favicon (an upward "deploy" arrow
// rising off a launch pad, on a rounded square) embedded as a base64 data
// URI, so it needs no extra static-file route or embed.FS entry. It swaps
// colors via an embedded @media query to track prefers-color-scheme the
// same way sharedStyle's --primary-bg/-fg pair does.
const faviconLink = `<link rel="icon" type="image/svg+xml" href="data:image/svg+xml;base64,PHN2ZyB4bWxucz0iaHR0cDovL3d3dy53My5vcmcvMjAwMC9zdmciIHZpZXdCb3g9IjAgMCAzMiAzMiI+PHN0eWxlPi5iZ3tmaWxsOiMxYTFhMWF9LmZne2ZpbGw6I2Y3ZjdmOH1AbWVkaWEgKHByZWZlcnMtY29sb3Itc2NoZW1lOmRhcmspey5iZ3tmaWxsOiNlNGU0ZTZ9LmZne2ZpbGw6IzE2MTcxYX19PC9zdHlsZT48cmVjdCBjbGFzcz0iYmciIHdpZHRoPSIzMiIgaGVpZ2h0PSIzMiIgcng9IjciLz48cGF0aCBjbGFzcz0iZmciIGQ9Ik0xNiA1IDI0IDE1IDIwIDE1IDIwIDIzIDEyIDIzIDEyIDE1IDggMTVaIi8+PHJlY3QgY2xhc3M9ImZnIiB4PSI5IiB5PSIyNiIgd2lkdGg9IjE0IiBoZWlnaHQ9IjMiLz48L3N2Zz4K">`

const sharedStyle = `
  :root {
    --bg: #f7f7f8;
    --fg: #1a1a1a;
    --muted: #666;
    --card-bg: #fff;
    --border: #eee;
    --border-strong: #ccc;
    --th-bg: #fafafa;
    --th-fg: #555;
    --shadow: rgba(0,0,0,0.08);
    --link: #2563eb;
    --row-hover: #f4f6fb;
    --button-bg: #fff;
    --button-fg: #1a1a1a;
    --button-hover: #f0f0f0;
    --primary-bg: #1a1a1a;
    --primary-fg: #fff;
    --primary-hover: #333;
    --danger: #b3261e;
    --ok-bg: #e6f4ea; --ok-fg: #2e7d32;
    --changes-bg: #fff3cd; --changes-fg: #8a6300; --changes-border: #f3d98a;
    --error-bg: #fde3e0; --error-fg: #b3261e;
    --running-bg: #e0e7ff; --running-fg: #3730a3;
    --awaiting-bg: #ede9fe; --awaiting-fg: #5b21b6;
    --warn-detail-bg: #fdf6e3; --warn-detail-fg: #6b5000;
    --confirm-bg: #eef2ff; --confirm-border: #c7d2fe;
  }
  @media (prefers-color-scheme: dark) {
    :root {
      --bg: #16171a;
      --fg: #e4e4e6;
      --muted: #9a9a9e;
      --card-bg: #1f2023;
      --border: #303136;
      --border-strong: #46474d;
      --th-bg: #26272b;
      --th-fg: #b2b2b7;
      --shadow: rgba(0,0,0,0.5);
      --link: #7aa2f7;
      --row-hover: #26272e;
      --button-bg: #26272b;
      --button-fg: #e4e4e6;
      --button-hover: #313236;
      --primary-bg: #e4e4e6;
      --primary-fg: #16171a;
      --primary-hover: #cfcfd2;
      --danger: #ff7a70;
      --ok-bg: #16321f; --ok-fg: #6fcf87;
      --changes-bg: #3a2f10; --changes-fg: #e0b34d; --changes-border: #5c4a15;
      --error-bg: #3a1715; --error-fg: #ff7a70;
      --running-bg: #21254a; --running-fg: #b0b8ff;
      --awaiting-bg: #2c2350; --awaiting-fg: #c9b6ff;
      --warn-detail-bg: #2a2410; --warn-detail-fg: #e0c880;
      --confirm-bg: #1c2036; --confirm-border: #333a63;
    }
  }
  body { font-family: -apple-system, Segoe UI, Roboto, sans-serif; margin: 2rem; background: var(--bg); color: var(--fg); }
  h1 { font-size: 1.4rem; margin-bottom: 0.2rem; }
  a { color: var(--link); }
  .meta { color: var(--muted); font-size: 0.85rem; margin-bottom: 1.5rem; }
  .table-wrap { overflow-x: auto; margin-top: 1rem; }
  table { border-collapse: collapse; width: 100%; min-width: 32rem; background: var(--card-bg); box-shadow: 0 1px 3px var(--shadow); }
  th, td { text-align: left; padding: 0.6rem 0.9rem; border-bottom: 1px solid var(--border); vertical-align: middle; }
  th { background: var(--th-bg); font-size: 0.8rem; text-transform: uppercase; letter-spacing: 0.03em; color: var(--th-fg); position: sticky; top: 0; z-index: 1; }
  tbody tr:hover { background: var(--row-hover); }
  .dir { font-family: ui-monospace, monospace; }
  button { font: inherit; cursor: pointer; border: 1px solid var(--border-strong); background: var(--button-bg); color: var(--button-fg); border-radius: 4px; padding: 0.35rem 0.8rem; margin-right: 0.3rem; }
  button:hover { background: var(--button-hover); }
  button.primary { background: var(--primary-bg); color: var(--primary-fg); border-color: var(--primary-bg); }
  button.primary:hover { background: var(--primary-hover); }
  button.danger { border-color: var(--danger); color: var(--danger); }
  .toolbar { display: flex; flex-wrap: wrap; gap: 0.5rem; margin-bottom: 1rem; align-items: center; }
  .badge { display: inline-block; padding: 0.15rem 0.5rem; border-radius: 4px; font-size: 0.75rem; font-weight: 600; }
  .badge-CHANGES { background: var(--changes-bg); color: var(--changes-fg); }
  .badge-ERROR { background: var(--error-bg); color: var(--error-fg); }
  .badge-OK { background: var(--ok-bg); color: var(--ok-fg); }
  .badge-running { background: var(--running-bg); color: var(--running-fg); }
  .badge-awaiting { background: var(--awaiting-bg); color: var(--awaiting-fg); }
  .badge-destroy { background: var(--error-bg); color: var(--error-fg); margin-left: 0.4rem; }
  .summary-bar { display: flex; flex-wrap: wrap; gap: 0.4rem; margin: 0.6rem 0 1rem; font-size: 0.8rem; color: var(--muted); align-items: center; }
  .notice { background: var(--changes-bg); color: var(--changes-fg); border: 1px solid var(--changes-border); border-radius: 4px; padding: 0.6rem 0.9rem; margin-bottom: 1rem; font-size: 0.88rem; }
  .progress-bar { background: var(--changes-border); border-radius: 4px; height: 0.5rem; margin-top: 0.5rem; overflow: hidden; }
  .progress-fill { background: var(--changes-fg); height: 100%; width: 0%; transition: width 0.3s ease; }
  #progress-text { font-size: 0.78rem; color: var(--changes-fg); margin-top: 0.3rem; }
  .resources { margin: 0.4rem 0 0; padding-left: 1.1rem; font-size: 0.82rem; }
  .resources li.create { color: var(--ok-fg); }
  .resources li.update { color: var(--changes-fg); }
  .resources li.replace { color: var(--changes-fg); }
  .resources li.delete { color: var(--error-fg); }
  .warnings { list-style: none; margin: 0.4rem 0 0; padding-left: 0; font-size: 0.82rem; color: var(--changes-fg); }
  .warnings li { padding: 0.2rem 0; }
  .warning-detail { margin: 0.3rem 0 0.2rem; padding: 0.5rem 0.7rem; background: var(--warn-detail-bg); border-left: 3px solid var(--changes-border); border-radius: 3px; font-size: 0.78rem; color: var(--warn-detail-fg); white-space: pre-wrap; overflow-wrap: anywhere; font-family: ui-monospace, monospace; }
  .attr-diff { list-style: none; margin: 0.25rem 0 0.4rem; padding-left: 0.9rem; border-left: 2px solid var(--border); font-size: 0.8rem; color: var(--fg); }
  .attr-diff li { padding: 0.1rem 0; }
  .attr-path { font-family: ui-monospace, monospace; color: var(--th-fg); }
  .attr-before { color: var(--error-fg); text-decoration: line-through; text-decoration-color: rgba(179,38,30,0.4); }
  .attr-after { color: var(--ok-fg); font-weight: 600; }
  .attr-arrow { color: var(--muted); }
  h2.section { font-size: 1.05rem; margin-top: 1.8rem; margin-bottom: 0; }
  .empty-state { padding: 2rem 0.9rem; text-align: center; color: var(--muted); background: var(--card-bg); box-shadow: 0 1px 3px var(--shadow); }

  .confirm-box { background: var(--confirm-bg); border: 1px solid var(--confirm-border); border-radius: 4px; padding: 0.7rem 0.9rem; margin: 0.6rem 0 0; font-size: 0.85rem; }
  .confirm-actions { margin-top: 0.6rem; display: flex; align-items: center; gap: 0.6rem; }
  .confirm-note { color: var(--muted); font-size: 0.78rem; }

  /* Status filter: pure CSS, no JavaScript — same hidden-radio +
     label[for] + general-sibling-combinator (~) trick as the static
     report's tabs. The radios, filter bar, and table-wrap must all be
     direct siblings in that order for the selectors below to reach the
     table. */
  input.status-filter-radio { position: absolute; opacity: 0; pointer-events: none; }
  .filter-bar { display: flex; flex-wrap: wrap; gap: 0.3rem; margin-bottom: 0.75rem; }
  .filter-bar label { padding: 0.3rem 0.75rem; border: 1px solid var(--border-strong); border-radius: 4px; cursor: pointer; font-size: 0.82rem; color: var(--th-fg); user-select: none; }
  #filter-OK:checked ~ .filter-bar label[for="filter-OK"],
  #filter-CHANGES:checked ~ .filter-bar label[for="filter-CHANGES"],
  #filter-ERROR:checked ~ .filter-bar label[for="filter-ERROR"],
  #filter-NONE:checked ~ .filter-bar label[for="filter-NONE"],
  #filter-all:checked ~ .filter-bar label[for="filter-all"] { background: var(--primary-bg); color: var(--primary-fg); border-color: var(--primary-bg); }
  #filter-OK:checked ~ .table-wrap tr.status-row:not(.status-OK),
  #filter-CHANGES:checked ~ .table-wrap tr.status-row:not(.status-CHANGES),
  #filter-ERROR:checked ~ .table-wrap tr.status-row:not(.status-ERROR),
  #filter-NONE:checked ~ .table-wrap tr.status-row:not(.status-NONE) { display: none; }

  @media (max-width: 640px) {
    body { margin: 1rem; }
    th, td { padding: 0.5rem 0.6rem; }
  }
`

var dashboardTmpl = template.Must(template.New("dashboard").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>homelab-deploy</title>
` + faviconLink + `
<style>` + sharedStyle + `</style>
</head>
<body>
<h1>homelab-deploy</h1>
<div class="toolbar">
  <a href="/graph"><button type="button">View Dependency Graph</button></a>
</div>
<div class="meta"><a href="/logs/report.html">last static report</a> (results from the last plan/apply run)</div>

{{if .Last}}{{if not .Current}}
<div class="meta">
  Last job: {{.Last.Kind}} started {{.Last.StartedAt.Format "15:04:05"}}
  {{if .Last.Err}}(failed){{else}}(done){{end}}
  &mdash; <a href="/jobs/{{.Last.ID}}">view it</a>
</div>
{{end}}{{end}}

{{if .Current}}
<div class="notice" id="progress-notice">
  <div id="progress-message">
  {{if .Current.AwaitingConfirmDir}}
  ⏸ apply paused &mdash; <code>{{.Current.AwaitingConfirmDir}}</code> has changes ready to apply &mdash;
    <a href="/jobs/{{.Current.ID}}">review &amp; confirm</a>
  {{else}}
  {{.Current.Kind}} running (started {{.Current.StartedAt.Format "15:04:05"}}) &mdash;
    <a href="/jobs/{{.Current.ID}}">view live output</a> if you want the detail.
  {{end}}
  </div>
  <div class="progress-bar"><div class="progress-fill" id="progress-fill"></div></div>
  <div id="progress-text">starting&hellip;</div>
</div>
<script>
(function() {
  var total = {{len .Current.Dirs}};
  var done = 0;
  var fill = document.getElementById('progress-fill');
  var text = document.getElementById('progress-text');
  var msg = document.getElementById('progress-message');
  var jobURL = '/jobs/{{.Current.ID}}';
  var es = new EventSource('/jobs/{{.Current.ID}}/stream');
  es.addEventListener('status', function() {
    done++;
    var pct = total > 0 ? Math.round((done / total) * 100) : 100;
    fill.style.width = pct + '%';
    text.textContent = done + ' / ' + total + ' directories done';
  });
  es.addEventListener('awaiting_confirmation', function(e) {
    var d = JSON.parse(e.data);
    msg.textContent = '';
    msg.appendChild(document.createTextNode('⏸ apply paused — '));
    var code = document.createElement('code');
    code.textContent = d.dir;
    msg.appendChild(code);
    msg.appendChild(document.createTextNode(' has changes ready to apply — '));
    var a = document.createElement('a');
    a.href = jobURL;
    a.textContent = 'review & confirm';
    msg.appendChild(a);
  });
  es.addEventListener('confirmation_resolved', function() {
    msg.textContent = '';
    msg.appendChild(document.createTextNode('{{.Current.Kind}}' + ' running — '));
    var a = document.createElement('a');
    a.href = jobURL;
    a.textContent = 'view live output';
    msg.appendChild(a);
  });
  es.addEventListener('job_done', function() {
    // Reload to pick up the finished job's results in the merged table
    // below and re-enable the buttons, rather than duplicating that
    // rendering logic in JS.
    location.reload();
  });
})();
</script>
{{end}}

<div class="toolbar">
  {{$busyTitle := ""}}{{if .Current}}{{$busyTitle = "a job is already running — wait for it to finish"}}{{end}}
  <form method="POST" action="/run">
    <input type="hidden" name="kind" value="plan">
    <button class="primary" type="submit"{{if .Current}} disabled title="{{$busyTitle}}"{{end}}>Plan All</button>
  </form>
  <form method="POST" action="/run">
    <input type="hidden" name="kind" value="apply">
    <button type="submit"{{if .Current}} disabled title="{{$busyTitle}}"{{end}}>Apply All</button>
  </form>
  <form method="POST" action="/run">
    <input type="hidden" name="kind" value="init">
    <button type="submit"{{if .Current}} disabled title="{{$busyTitle}}"{{end}}>Init All</button>
  </form>
  {{if .MultiRepo}}
  <form method="POST" action="/run">
    <input type="hidden" name="kind" value="sync">
    <button type="submit"{{if .Current}} disabled title="{{$busyTitle}}"{{end}}>Sync All</button>
  </form>
  {{end}}
</div>

{{if .Dirs}}
<div class="summary-bar">
  {{len .Dirs}} director{{if eq (len .Dirs) 1}}y{{else}}ies{{end}}
  {{if .Counts.OK}}&middot; <span class="badge badge-OK">{{.Counts.OK}} OK</span>{{end}}
  {{if .Counts.Changes}}&middot; <span class="badge badge-CHANGES">{{.Counts.Changes}} changes</span>{{end}}
  {{if .Counts.Error}}&middot; <span class="badge badge-ERROR">{{.Counts.Error}} error</span>{{end}}
  {{if .Counts.None}}&middot; {{.Counts.None}} no result{{end}}
</div>
{{end}}

<input class="status-filter-radio" type="radio" id="filter-all" name="statusfilter" checked>
<input class="status-filter-radio" type="radio" id="filter-OK" name="statusfilter">
<input class="status-filter-radio" type="radio" id="filter-CHANGES" name="statusfilter">
<input class="status-filter-radio" type="radio" id="filter-ERROR" name="statusfilter">
<input class="status-filter-radio" type="radio" id="filter-NONE" name="statusfilter">

<div class="filter-bar">
  <label for="filter-all">All ({{len .Dirs}})</label>
  <label for="filter-OK">OK ({{.Counts.OK}})</label>
  <label for="filter-CHANGES">Changes ({{.Counts.Changes}})</label>
  <label for="filter-ERROR">Error ({{.Counts.Error}})</label>
  <label for="filter-NONE">No result ({{.Counts.None}})</label>
</div>

{{if not .Dirs}}
<div class="empty-state">No Terraform directories found under this repo yet.</div>
{{else}}
<div class="table-wrap">
<table>
<thead><tr><th>Status</th><th>Directory</th><th>Detail</th><th>Actions</th></tr></thead>
<tbody>
{{$current := .Current}}
{{$entries := .LastEntries}}
{{range .Dirs}}
{{$dir := .}}
{{$entry := index $entries $dir}}
<tr class="status-row status-{{if $entry.Status}}{{$entry.Status}}{{else}}NONE{{end}}">
  <td>{{if $entry.Status}}<span class="badge badge-{{$entry.Status}}">{{$entry.Status}}</span>{{else}}—{{end}}</td>
  <td class="dir">{{$dir}}</td>
  <td>
    {{if $entry.Status}}
    {{$entry.Detail}}{{if $entry.Destroys}}<span class="badge badge-destroy">&#9888; {{$entry.Destroys}} to destroy</span>{{end}}
    {{if $entry.ResourceChanges}}
    <ul class="resources">
      {{range $entry.ResourceChanges}}<li class="{{.Action}}">{{.Action}}: {{.Address}}
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
    {{if $entry.Warnings}}
    <ul class="warnings">
      {{range $entry.Warnings}}<li>&#9888; {{.Summary}}{{if .Detail}}<pre class="warning-detail">{{.Detail}}</pre>{{end}}</li>{{end}}
    </ul>
    {{end}}
    {{if $entry.RemotePlanURL}}<div><a href="{{$entry.RemotePlanURL}}" target="_blank" rel="noopener">view this run in Terraform Cloud</a></div>{{end}}
    {{else}}—{{end}}
  </td>
  <td>
    <form method="POST" action="/run" style="display:inline">
      <input type="hidden" name="kind" value="plan">
      <input type="hidden" name="dir" value="{{$dir}}">
      <button type="submit"{{if $current}} disabled title="{{$busyTitle}}"{{end}}>Plan</button>
    </form>
    <form method="POST" action="/run" style="display:inline">
      <input type="hidden" name="kind" value="apply">
      <input type="hidden" name="dir" value="{{$dir}}">
      <button type="submit"{{if $current}} disabled title="{{$busyTitle}}"{{end}}>Apply</button>
    </form>
    <form method="POST" action="/run" style="display:inline">
      <input type="hidden" name="kind" value="init">
      <input type="hidden" name="dir" value="{{$dir}}">
      <button type="submit"{{if $current}} disabled title="{{$busyTitle}}"{{end}}>Init</button>
    </form>
  </td>
</tr>
{{end}}
</tbody>
</table>
</div>
{{end}}
</body>
</html>
`))

var jobTmpl = template.Must(template.New("job").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Kind}} — {{.ID}}</title>
` + faviconLink + `
<style>` + sharedStyle + `
  .panes { display: flex; flex-direction: column; gap: 1rem; margin-top: 1rem; }
  .pane { background: var(--card-bg); border-radius: 4px; box-shadow: 0 1px 3px var(--shadow); overflow: hidden; }
  .pane-header { display: flex; justify-content: space-between; align-items: center; padding: 0.5rem 0.9rem; border-bottom: 1px solid var(--border); background: var(--th-bg); position: sticky; top: 0; z-index: 1; }
  .pane-header code { font-size: 0.85rem; }
  .pane pre { margin: 0; padding: 0.8rem; background: #1e1e1e; color: #ddd; font-size: 0.8rem; max-height: 320px; overflow: auto; white-space: pre-wrap; overflow-wrap: anywhere; }
  .banner { display: none; border-radius: 4px; padding: 0.7rem 1rem; margin: 1rem 0; font-size: 0.9rem; }
  .banner-ok { background: var(--ok-bg); color: var(--ok-fg); }
  .banner-error { background: var(--error-bg); color: var(--error-fg); }
  .banner-warn { background: var(--changes-bg); color: var(--changes-fg); }
  .confirm-alert { position: sticky; top: 0; z-index: 3; }
</style>
</head>
<body>
<h1>{{.Kind}} <span style="color:var(--muted);font-weight:400">— job {{.ID}}</span></h1>
<div class="meta">Started {{.StartedAt.Format "15:04:05"}} &middot; <a href="/">dashboard</a> &middot; <a href="/logs/report.html">static report</a>{{if .Running}} &middot; <form style="display:inline" method="POST" action="/jobs/{{.ID}}/cancel" onsubmit="return confirm('Cancel this job? The currently-running terraform command will be killed.');"><button class="danger" type="submit">Cancel job</button></form>{{end}}</div>

<div id="banner" class="banner"></div>
<div id="confirm-alert" class="notice confirm-alert"{{if not .AwaitingConfirmDir}} hidden{{end}}>
  ⏸ apply paused — <code id="confirm-alert-dir">{{.AwaitingConfirmDir}}</code> is awaiting confirmation —
  <a href="#" id="confirm-alert-link">jump to it</a>
</div>

<div class="panes">
{{$job := .}}
{{range .Dirs}}
{{$dir := .}}
<div class="pane" data-dir="{{$dir}}">
  <div class="pane-header"><code>{{$dir}}</code> {{if eq $dir $job.AwaitingConfirmDir}}<span class="badge badge-awaiting">awaiting confirmation</span>{{else}}<span class="badge badge-running">running…</span>{{end}}</div>
  <ul class="warnings"></ul>
  <pre class="out"></pre>
  {{if eq $job.Kind "apply"}}
  <div class="confirm-box"{{if ne $dir $job.AwaitingConfirmDir}} hidden{{end}}>
    <div class="confirm-diff">
      {{if eq $dir $job.AwaitingConfirmDir}}
      {{with $job.AwaitingConfirmPreview}}
      {{if .Detail}}<div>{{.Detail}}</div>{{end}}
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
      {{if .RawPlanText}}<pre>{{.RawPlanText}}</pre>{{end}}
      {{if .RemotePlanURL}}<div><a href="{{.RemotePlanURL}}" target="_blank" rel="noopener">view this run in Terraform Cloud</a></div>{{end}}
      {{end}}
      {{end}}
    </div>
    <div class="confirm-actions">
      <form method="POST" action="/jobs/{{$job.ID}}/confirm" style="display:inline">
        <input type="hidden" name="dir" value="{{$dir}}">
        <button class="primary" type="submit">Confirm Apply</button>
      </form>
      <span class="confirm-note">or Cancel job above to abort</span>
    </div>
  </div>
  {{end}}
</div>
{{end}}
</div>

<script>
(function() {
  // Recognizes a diagnostic header line ("Warning: ..."/"Error: ...")
  // and the horizontal-rule divider Terraform prints once, right after
  // its diagnostics section \u2014 see parseWarnings/diagnosticDivider in
  // internal/tf/tf.go, which this mirrors line-for-line so a warning
  // streamed live during apply (which never gets a backend-parsed
  // Warnings list \u2014 only plan does) looks the same as one delivered via
  // the 'status' event below.
  var diagnosticHeaderRe = /^(Warning|Error): (.*)$/;
  var dividerRe = /^\u2500+$/;

  var panes = {};
  document.querySelectorAll('.pane').forEach(function(el) {
    panes[el.dataset.dir] = {
      pane: el,
      badge: el.querySelector('.badge'),
      out: el.querySelector('.out'),
      warnList: el.querySelector('.warnings'),
      confirmBox: el.querySelector('.confirm-box'),
      confirmDiff: el.querySelector('.confirm-diff'),
      warningsSeen: new Set(),
      lineBuf: '',
      inWarning: false,
      curSummary: '',
      curDetailLines: []
    };
  });

  function appendOutput(dir, text) {
    var p = panes[dir];
    if (!p) return;
    p.out.appendChild(document.createTextNode(text));
    p.out.scrollTop = p.out.scrollHeight;
  }

  function addWarning(p, summary, detail) {
    if (p.warningsSeen.has(summary)) return;
    p.warningsSeen.add(summary);
    var li = document.createElement('li');
    li.textContent = '\u26a0 ' + summary;
    if (detail) {
      var pre = document.createElement('pre');
      pre.className = 'warning-detail';
      pre.textContent = detail;
      li.appendChild(pre);
    }
    p.warnList.appendChild(li);
  }

  function finalizeWarning(p) {
    if (!p.inWarning) return;
    var lines = p.curDetailLines;
    var start = 0, end = lines.length;
    while (start < end && lines[start] === '') start++;
    while (end > start && lines[end - 1] === '') end--;
    addWarning(p, p.curSummary, lines.slice(start, end).join('\n'));
    p.inWarning = false;
    p.curSummary = '';
    p.curDetailLines = [];
  }

  function processLine(p, line) {
    var m = diagnosticHeaderRe.exec(line);
    if (m) {
      finalizeWarning(p);
      if (m[1] === 'Warning') {
        p.inWarning = true;
        p.curSummary = m[2];
        p.curDetailLines = [];
      }
      return;
    }
    if (dividerRe.test(line)) {
      finalizeWarning(p);
      return;
    }
    if (p.inWarning) p.curDetailLines.push(line);
  }

  // Scans terraform's live output for warning diagnostics as they
  // stream in, so a warning shows up immediately rather than waiting
  // for the directory to finish \u2014 the only way to catch one during
  // apply. Chunk boundaries don't align with line boundaries, so each
  // pane keeps its own trailing partial line across calls.
  function scanForWarnings(dir, text) {
    var p = panes[dir];
    if (!p) return;
    p.lineBuf += text;
    var lines = p.lineBuf.split('\n');
    p.lineBuf = lines.pop();
    lines.forEach(function(line) { processLine(p, line); });
  }

  function setStatus(dir, status, detail, remotePlanUrl, warnings) {
    var p = panes[dir];
    if (!p) return;
    // The directory is done \u2014 flush the last streamed line (if any)
    // and finalize a still-open warning rather than losing it because
    // no trailing divider ever arrived.
    if (p.lineBuf) { processLine(p, p.lineBuf); p.lineBuf = ''; }
    finalizeWarning(p);
    p.badge.textContent = status + (detail ? ' \u2014 ' + detail : '');
    p.badge.className = 'badge badge-' + status;
    if (remotePlanUrl) {
      var link = document.createElement('a');
      link.href = remotePlanUrl;
      link.target = '_blank';
      link.rel = 'noopener';
      link.textContent = 'view this run in Terraform Cloud';
      link.style.marginLeft = '0.6rem';
      p.badge.after(link);
    }
    (warnings || []).forEach(function(w) { addWarning(p, w.summary, w.detail); });
  }

  // Builds the same .resources/.attr-diff/.warnings markup the Go
  // templates render server-side (see the confirm-box block above and
  // report.go's htmlTmpl), from the JSON an 'awaiting_confirmation'
  // event carries — needed here too since that event can arrive for a
  // directory other than the one the page happened to load already
  // paused on. textContent throughout, same as addWarning above: never
  // innerHTML on terraform-sourced text.
  function renderConfirmBox(container, d) {
    while (container.firstChild) container.removeChild(container.firstChild);
    if (d.detail) {
      var det = document.createElement('div');
      det.textContent = d.detail;
      container.appendChild(det);
    }
    if (d.resourceChanges && d.resourceChanges.length) {
      var ul = document.createElement('ul');
      ul.className = 'resources';
      d.resourceChanges.forEach(function(rc) {
        var li = document.createElement('li');
        li.className = rc.Action;
        li.appendChild(document.createTextNode(rc.Action + ': ' + rc.Address));
        if (rc.Attributes && rc.Attributes.length) {
          var attrUl = document.createElement('ul');
          attrUl.className = 'attr-diff';
          rc.Attributes.forEach(function(a) {
            var attrLi = document.createElement('li');
            var code = document.createElement('code');
            code.className = 'attr-path';
            code.textContent = a.Path;
            var before = document.createElement('span');
            before.className = 'attr-before';
            before.textContent = a.Before;
            var arrow = document.createElement('span');
            arrow.className = 'attr-arrow';
            arrow.textContent = '→';
            var after = document.createElement('span');
            after.className = 'attr-after';
            after.textContent = a.After;
            attrLi.appendChild(code);
            attrLi.appendChild(document.createTextNode(': '));
            attrLi.appendChild(before);
            attrLi.appendChild(document.createTextNode(' '));
            attrLi.appendChild(arrow);
            attrLi.appendChild(document.createTextNode(' '));
            attrLi.appendChild(after);
            attrUl.appendChild(attrLi);
          });
          li.appendChild(attrUl);
        }
        ul.appendChild(li);
      });
      container.appendChild(ul);
    }
    if (d.warnings && d.warnings.length) {
      var wUl = document.createElement('ul');
      wUl.className = 'warnings';
      d.warnings.forEach(function(w) {
        var li = document.createElement('li');
        li.textContent = '⚠ ' + w.summary;
        if (w.detail) {
          var pre = document.createElement('pre');
          pre.className = 'warning-detail';
          pre.textContent = w.detail;
          li.appendChild(pre);
        }
        wUl.appendChild(li);
      });
      container.appendChild(wUl);
    }
    if (d.rawPlanText) {
      var pre = document.createElement('pre');
      pre.textContent = d.rawPlanText;
      container.appendChild(pre);
    }
    if (d.remotePlanUrl) {
      var div = document.createElement('div');
      var a = document.createElement('a');
      a.href = d.remotePlanUrl;
      a.target = '_blank';
      a.rel = 'noopener';
      a.textContent = 'view this run in Terraform Cloud';
      div.appendChild(a);
      container.appendChild(div);
    }
  }

  // Pulls the eye (and, if the tab isn't even focused, the title bar) to
  // whichever pane is actually waiting on a human right now — with many
  // directories in one run, that pane can easily be scrolled out of
  // view, or the whole tab backgrounded, while terraform's own output
  // keeps quietly scrolling by elsewhere on the page.
  var confirmAlert = document.getElementById('confirm-alert');
  var confirmAlertDir = document.getElementById('confirm-alert-dir');
  var confirmAlertLink = document.getElementById('confirm-alert-link');
  var baseTitle = document.title;

  function showConfirmAlert(dir) {
    confirmAlertDir.textContent = dir;
    confirmAlertLink.onclick = function(e) {
      e.preventDefault();
      var p = panes[dir];
      if (p) p.pane.scrollIntoView({behavior: 'smooth', block: 'start'});
    };
    confirmAlert.hidden = false;
    document.title = '⏸ needs confirmation — ' + baseTitle;
  }

  function hideConfirmAlert() {
    confirmAlert.hidden = true;
    document.title = baseTitle;
  }

  // The banner may already be rendered server-side (a page load/reload
  // while a directory is mid-pause) — wire up its link and title in that
  // case too, not just on the live SSE event below.
  if (!confirmAlert.hidden) showConfirmAlert(confirmAlertDir.textContent);

  var es = new EventSource('/jobs/{{.ID}}/stream');

  es.addEventListener('awaiting_confirmation', function(e) {
    var d = JSON.parse(e.data);
    var p = panes[d.dir];
    if (!p || !p.confirmBox) return;
    p.badge.textContent = 'awaiting confirmation';
    p.badge.className = 'badge badge-awaiting';
    renderConfirmBox(p.confirmDiff, d);
    p.confirmBox.hidden = false;
    showConfirmAlert(d.dir);
  });

  es.addEventListener('confirmation_resolved', function(e) {
    var d = JSON.parse(e.data);
    var p = panes[d.dir];
    if (!p) return;
    if (p.confirmBox) p.confirmBox.hidden = true;
    p.badge.textContent = 'running…';
    p.badge.className = 'badge badge-running';
    hideConfirmAlert();
  });

  es.addEventListener('output', function(e) {
    var d = JSON.parse(e.data);
    appendOutput(d.dir, d.text);
    scanForWarnings(d.dir, d.text);
  });

  es.addEventListener('status', function(e) {
    var d = JSON.parse(e.data);
    setStatus(d.dir, d.status, d.detail, d.remotePlanUrl, d.warnings);
  });

  // Fired when the server had to drop output frames — either they
  // aged out of the replay buffer before this page ever loaded, or a
  // slow connection couldn't keep up live. There's no way to recover
  // the missing lines, so this just makes their absence visible instead
  // of the pane silently looking complete when it isn't.
  es.addEventListener('gap', function(e) {
    var d = JSON.parse(e.data);
    var banner = document.getElementById('banner');
    banner.style.display = 'block';
    banner.className = 'banner banner-warn';
    banner.textContent = '⚠ ' + d.dropped + ' output update(s) were dropped (buffer overflow or a slow connection) — some output above may be incomplete.';
  });

  es.addEventListener('job_done', function(e) {
    hideConfirmAlert();
    var d = JSON.parse(e.data);
    var banner = document.getElementById('banner');
    banner.style.display = 'block';
    if (d.canceled) {
      banner.className = 'banner banner-error';
      banner.textContent = 'Job canceled.' + (d.err ? ' (' + d.err + ')' : '');
    } else if (d.err) {
      banner.className = 'banner banner-error';
      banner.textContent = 'Job failed: ' + d.err;
    } else {
      banner.className = 'banner banner-ok';
      banner.textContent = 'Job complete.';
    }
    // The Cancel button/form only rendered server-side while the job was
    // still running — remove it now rather than leaving a stale button
    // that would just redirect to an already-finished job.
    var cancelForm = document.querySelector('form[action$="/cancel"]');
    if (cancelForm) cancelForm.remove();
    es.close();
  });

  es.onerror = function() {
    // The browser's EventSource auto-retries on transient network
    // drops; if the job has actually finished the broadcaster closes
    // the stream cleanly (no error), so an error here means a real
    // connection problem, not job completion. Leave it to auto-retry
    // rather than showing a scary error for a blip.
  };
})();
</script>
</body>
</html>
`))
