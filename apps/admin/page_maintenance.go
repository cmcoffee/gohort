package admin

import (
	"fmt"
	"html"
	"net/url"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

// maintenanceSections is the maintenance part of the admin page: Scheduled Tasks, the maintenance groups, Migrations, Vector Index (with its repairs), Database Browser.
func (a *AdminApp) maintenanceSections() []ui.Section {
	return []ui.Section{
		// First, and only when there is something to say. A store that has
		// been failing is the fact that explains every other oddity on this
		// page, and it used to be unreportable: the process ended at the first
		// failure, so there was never an "after" in which to show it.
		storeHealthSection(),
		{
			Title:    "Scheduled Tasks",
			Subtitle: "Pending background work: proactive messages, scheduled updates. Expand a row for the full record + payload.",
			Body: ui.Table{
				Source: "api/scheduled-tasks",
				RowKey: "id",
				Columns: []ui.Col{
					{Field: "kind", Flex: 1},
					// What the task actually is (e.g. an event monitor's name +
					// the agent it wakes), from the task-describer registry.
					// Blank for kinds without a describer.
					{Field: "detail", Flex: 2},
					// Future-aware relative time: "in 5m" while pending,
					// flips to "5m ago" if the worker missed firing it.
					{Field: "run_at", Format: "fromnow", Mute: true},
				},
				RowActions: []ui.RowAction{
					// One combined "Details" expand showing the full
					// record AND the payload JSON, instead of two
					// adjacent buttons. Stack composes them in one panel.
					ui.Expand("Details", ui.Stack{
						Children: []ui.Component{
							ui.RecordView{
								Pairs: []ui.DisplayPair{
									{Label: "ID", Field: "id", Mono: true},
									{Label: "Kind", Field: "kind"},
									{Label: "Detail", Field: "detail"},
									{Label: "Fires", Field: "run_at", Format: "fromnow"},
									{Label: "Run at (UTC)", Field: "run_at", Mono: true},
									{Label: "Created", Field: "created", Format: "reltime"},
								},
							},
							ui.JSONView{Field: "payload", Title: "Task payload"},
						},
					}),
					{
						Type: "button", Label: "Cancel",
						PostTo:  "api/scheduled-tasks?id={id}",
						Method:  "DELETE",
						Variant: "warning",
						Confirm: "Cancel this scheduled task?",
					},
				},
				AutoRefreshMS: 30000,
				EmptyText:     "No tasks scheduled.",
			},
		},
		// Maintenance buttons are laid out by the group each registrant
		// declared, one section per group. A flat list of fourteen "Run"
		// buttons read as fourteen equal things when three were dangerous, two
		// were reports, and most were for a day that had not come.
		//
		// The GROUPING is what fixed that. These were also closed by default,
		// on the theory that the rarely-used ones should stay out of the way;
		// the user asked twice for them open, so they are open. A section you
		// have to click to discover is a section you do not know is there, and
		// the heading above each one already says what it is.
		{
			Title:    "Reclaim space",
			Subtitle: "Each dry run lists exactly what the delete beneath it would remove.",
			Detail:   "Run the dry run first: deletes are permanent.",
			Body:     maintenanceList("Reclaim space", "Nothing to reclaim is registered."),
		},
		{
			Title:    "Reports",
			Subtitle: "Read-only surveys of what is on disk and what tools depend on. Change nothing.",
			Body:     maintenanceList("Reports", "No reports registered."),
		},
		{
			Title:    "Housekeeping",
			Subtitle: "One-shot operations that fix stale state, or run a scheduled job now.",
			Detail:   "Each runs in the background and reports the number of records touched.",
			Body:     maintenanceList("Housekeeping", "No housekeeping functions registered."),
		},
		{
			Title:    "Migrations",
			Subtitle: "Schema and data migrations the apps have run on this deployment, and the ones an operator runs by hand.",
			Detail:   "Most auto-fire on app init when triggered, with no manual button, and never run twice for the same (app, name, owner). An error column indicates a panic during the run: clear the marker in the DB to retry after a fix.\n\nThe ones listed beneath the table are the exception: a migration that has to be run deliberately, because it rewrites records an operator should be watching when it happens.",
			Body: ui.Stack{Children: []ui.Component{
				ui.Table{
					Source: "api/migrations",
					RowKey: "key",
					Columns: []ui.Col{
						{Field: "app", Label: "App", Flex: 1},
						{Field: "name", Label: "Migration", Flex: 2},
						{Field: "owner", Label: "Owner", Mute: true},
						{Field: "ran_at", Label: "Ran", Format: "reltime", Mute: true},
						{Field: "changed", Label: "Changed", Mute: true},
						{Field: "error", Label: "Error", Mute: true},
					},
					EmptyText: "No migrations have run on this deployment yet.",
				},
				// The hand-run ones, under the record of the automatic ones.
				//
				// This was missing, and nothing said so. A maintenance function
				// registers itself under a GROUP, and the group only reaches a
				// screen if some section calls maintenanceList for it — so the
				// one registered under "Migrations" had no button anywhere, and
				// the note telling an operator to go and click it described a
				// path that did not exist. It sat unrun for a fortnight while
				// the exemptions it was meant to move stayed inert.
				maintenanceList("Migrations", "No migrations need to be run by hand."),
			}},
		},
		{
			Title:    "Vector Index",
			Subtitle: "Snapshot of the semantic-search index.",
			Detail:   "Chunks are written automatically as records are produced, whether research, debate or answer.\n\nA chunk whose embedding failed at ingest, or was embedded under a different model or document prefix, is still stored and still found by keyword but invisible to semantic search. The counts below say how many, and Repair below them fixes it.\n\nA chunk with no TEXT is counted apart. Repair cannot fix it, there being nothing to embed, and search cannot return it, so it is dead weight; remove it.",
			Body: ui.Stack{Children: []ui.Component{
				ui.DisplayPanel{
					Source: "api/vector-stats",
					Pairs: []ui.DisplayPair{
						{Label: "Total chunks", Field: "total"},
						{Label: "Embedded", Field: "embedded"},
						{Label: "Empty (embed failed)", Field: "empty"},
						// Which sources the gap is in. A bare count says an
						// outage happened; this says what it cost, and which
						// imports to re-run for anything the repair can't reach.
						{Label: "Missing vectors by source", Field: "empty_by_source_text"},
						// Vectors that exist but were made in another space: a
						// model or document-prefix change since ingest. Semantic
						// search skips them until the stale pass runs.
						{Label: "In another embedding space", Field: "stale"},
						{Label: "Stale vectors by source", Field: "stale_by_source_text"},
						// Rows with no text: not a gap the repair can close,
						// which is why they never left the counts.
						{Label: "Unusable (no text)", Field: "unusable"},
						{Label: "Unusable by source", Field: "unusable_by_source_text"},
					},
				},
				// Per-kind breakdown (documents + chunks) — the legible view,
				// vs the opaque per-source id dump it replaces.
				ui.Table{
					Source: "api/vector-stats/by-kind",
					RowKey: "kind",
					Columns: []ui.Col{
						{Field: "label", Label: "Source", Flex: 2},
						{Field: "documents", Label: "Documents"},
						{Field: "chunks", Label: "Chunks"},
					},
					EmptyText: "No chunks indexed yet.",
				},
				// The repairs sit under the counts they repair.
				maintenanceList("Vector index", "No index repairs registered."),
			}},
		},
		{
			// Open, not collapsed: the browser is what an operator comes to
			// this page for, and a closed card hid it behind a caret.
			Title:    "Database Browser",
			Subtitle: "Read-only view of the server database. Click a table to list its keys, click a key to inspect the record.",
			Body:     databaseBrowserCard(),
		},
	}
}

// maintenanceList is the Run-button list for one maintenance group.
func maintenanceList(group, empty string) ui.ActionList {
	return ui.ActionList{
		Source:     "api/maintenance?group=" + url.QueryEscape(group),
		LabelField: "Label",
		DescField:  "Desc",
		PostTo:     "api/maintenance?key={Key}",
		Method:     "POST",
		// When it last ran, and by whom. The question to settle before any
		// question about cadence: a schedule for a pass whose staleness nobody
		// can see is a guess with a cron on it.
		HistoryField: "History",
		ButtonText:   "Run",
		EmptyText:    empty,
		// A pass that walks the whole store takes minutes; this is where it
		// says how far along it is (see core.ReportMaintenanceProgress).
		ProgressSource: "api/maintenance/progress?key={Key}",
	}
}

func databaseBrowserCard() ui.Card {
	return ui.Card{HTML: `
<style>
.dbb { display:flex; gap:0.75rem; margin-top:0.25rem; min-height:200px; }
.dbb-pane { display:flex; flex-direction:column; min-width:0; }
.dbb-label { font-size:0.72rem; color:var(--text-mute,#8b949e); text-transform:uppercase; letter-spacing:0.05em; margin-bottom:0.35rem; white-space:nowrap; overflow:hidden; text-overflow:ellipsis; }
.dbb-list { background:var(--bg-0,#0d1117); border:1px solid var(--border,#30363d); border-radius:6px; overflow-y:auto; max-height:380px; flex:1; }
.dbb-item { padding:0.35rem 0.6rem; font-size:0.8rem; color:var(--text,#c9d1d9); border-bottom:1px solid #161b22; cursor:pointer; word-break:break-all; line-height:1.4; }
.dbb-item:last-child { border-bottom:none; }
.dbb-item:hover { background:#21262d; }
.dbb-item.active { background:#1f3047; color:#79c0ff; }
.dbb-empty { padding:0.5rem 0.6rem; font-size:0.8rem; color:#8b949e; font-style:italic; }
.dbb-record { background:var(--bg-0,#0d1117); border:1px solid var(--border,#30363d); border-radius:6px; padding:0.6rem 0.75rem; overflow:auto; max-height:380px; font-size:0.78rem; color:var(--text,#c9d1d9); margin:0; white-space:pre; font-family:monospace; line-height:1.5; }
</style>
<div class="dbb">
  <div class="dbb-pane" style="width:180px;flex-shrink:0">
    <div class="dbb-label">Tables</div>
    <div class="dbb-list" id="dbb-tables"><div class="dbb-empty">Loading...</div></div>
  </div>
  <div class="dbb-pane" id="dbb-keys-pane" style="width:200px;flex-shrink:0;display:none">
    <div class="dbb-label" id="dbb-keys-label">Keys</div>
    <div class="dbb-list" id="dbb-keys"></div>
  </div>
  <div class="dbb-pane" id="dbb-rec-pane" style="flex:1;display:none">
    <div class="dbb-label" id="dbb-rec-label">Record</div>
    <pre class="dbb-record" id="dbb-rec"></pre>
  </div>
</div>
<script>
(function(){
  var activeTable = '';
  function mkItem(text, onclick){
    var d = document.createElement('div');
    d.className = 'dbb-item';
    d.textContent = text;
    d.addEventListener('click', function(){ onclick(d); });
    return d;
  }
  function clearActive(sel){ document.querySelectorAll(sel).forEach(function(e){ e.classList.remove('active'); }); }
  function loadTables(){
    fetch('api/db/tables').then(function(r){ return r.json(); }).then(function(tables){
      var list = document.getElementById('dbb-tables');
      list.innerHTML = '';
      if(!tables || !tables.length){ list.innerHTML = '<div class="dbb-empty">No tables found.</div>'; return; }
      tables.forEach(function(t){ list.appendChild(mkItem(t, function(el){ selectTable(t, el); })); });
    }).catch(function(){ var l=document.getElementById('dbb-tables'); if(l) l.innerHTML='<div class="dbb-empty">Failed to load tables.</div>'; });
  }
  function selectTable(table, el){
    activeTable = table;
    clearActive('#dbb-tables .dbb-item');
    if(el) el.classList.add('active');
    document.getElementById('dbb-keys-pane').style.display = '';
    document.getElementById('dbb-rec-pane').style.display = 'none';
    document.getElementById('dbb-keys-label').textContent = table;
    var keyList = document.getElementById('dbb-keys');
    keyList.innerHTML = '<div class="dbb-empty">Loading...</div>';
    fetch('api/db/keys?table=' + encodeURIComponent(table)).then(function(r){ return r.json(); }).then(function(keys){
      keyList.innerHTML = '';
      if(!keys || !keys.length){ keyList.innerHTML = '<div class="dbb-empty">No keys.</div>'; return; }
      keys.forEach(function(k){ keyList.appendChild(mkItem(k, function(el){ loadRecord(k, el); })); });
    }).catch(function(){ keyList.innerHTML = '<div class="dbb-empty">Failed to load keys.</div>'; });
  }
  function loadRecord(key, el){
    clearActive('#dbb-keys .dbb-item');
    if(el) el.classList.add('active');
    document.getElementById('dbb-rec-pane').style.display = '';
    document.getElementById('dbb-rec-label').textContent = key;
    var view = document.getElementById('dbb-rec');
    view.textContent = 'Loading...';
    fetch('api/db/record?table=' + encodeURIComponent(activeTable) + '&key=' + encodeURIComponent(key)).then(function(r){
      if(!r.ok) return r.text().then(function(t){ throw new Error(t); });
      return r.json();
    }).then(function(v){ view.textContent = JSON.stringify(v, null, 2); }).catch(function(err){ view.textContent = 'Error: ' + err.message; });
  }
  loadTables();
})();
</script>
`}
}

// storeHealthSection reports store failures, and renders as nothing at all
// while there have been none.
//
// Not a green tick. A panel that says "healthy" on every visit is a panel
// people stop reading, and the one time it matters it has to compete with its
// own history of saying nothing was wrong.
func storeHealthSection() ui.Section {
	h := DBHealth()
	if h.Reads == 0 && h.Writes == 0 {
		return ui.Section{}
	}
	title := "The database has been failing"
	if h.Writes > 0 {
		// Said differently on purpose: a read that fails degrades an answer, a
		// write that fails loses somebody's work, and the second deserves the
		// louder heading.
		title = "The database has been losing writes"
	}
	return ui.Section{
		Title:    title,
		Subtitle: "The server stayed up and kept serving.",
		Detail: "Which is why you are reading this rather than finding it in a crash log." +
			"A failed read reaches its caller as 'not found', so missing records and empty lists elsewhere on this page may be this and not the truth.",
		Body: ui.Card{HTML: html.EscapeString(storeHealthLine(h))},
	}
}

func storeHealthLine(h DBFailureReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d failed write(s), %d failed read(s).", h.Writes, h.Reads)
	if !h.Since.IsZero() {
		fmt.Fprintf(&b, " First in this run %s ago", time.Since(h.Since).Round(time.Second))
		if !h.LastAt.IsZero() {
			fmt.Fprintf(&b, ", most recent %s ago", time.Since(h.LastAt).Round(time.Second))
		}
		b.WriteString(".")
	}
	if h.LastOp != "" {
		fmt.Fprintf(&b, " Last: %s, %s", h.LastOp, h.Last)
	}
	b.WriteString(" The key each failure hit is in the server log; it is left out here because it names somebody's record.")
	return b.String()
}
