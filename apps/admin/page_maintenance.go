package admin

import (
	"github.com/cmcoffee/gohort/core/ui"
)

// maintenanceSections is the maintenance part of the admin page: Scheduled Tasks, Maintenance, Migrations, Vector Index, Database Browser.
func (a *AdminApp) maintenanceSections() []ui.Section {
	return []ui.Section{
		{
			Title:    "Scheduled Tasks",
			Subtitle: "Pending background work — proactive messages, scheduled updates. Expand a row for the full record + payload.",
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
		{
			Title:    "Maintenance",
			Subtitle: "One-shot operations that fix stale state or rebuild derived data. Each runs in the background and reports the number of records touched.",
			Body: ui.ActionList{
				Source:     "api/maintenance",
				LabelField: "Label",
				DescField:  "Desc",
				PostTo:     "api/maintenance?key={Key}",
				Method:     "POST",
				ButtonText: "Run",
				EmptyText:  "No maintenance functions registered.",
			},
		},
		{
			Title:     "Migrations",
			Subtitle:  "Schema / data migrations the apps have run on this deployment. Auto-fire on app init when triggered (no manual button) and never run twice for the same (app, name, owner). An error column indicates a panic during the run — clear the marker in the DB to retry after a fix.",
			Collapsed: true,
			Body: ui.Table{
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
		},
		{
			Title:    "Vector Index",
			Subtitle: "Snapshot of the semantic-search index. Chunks are written automatically as records (research / debate / answer) are produced. A chunk whose embedding failed at ingest is still stored and still found by keyword, but is invisible to semantic search until it is re-embedded — run \"Re-embed chunks missing a vector\" under Maintenance to repair those.",
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
			}},
		},
		{
			Title:     "Database Browser",
			Subtitle:  "Read-only view of the server database. Click a table to list its keys, click a key to inspect the record.",
			Collapsed: true,
			Body:      databaseBrowserCard(),
		},
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
