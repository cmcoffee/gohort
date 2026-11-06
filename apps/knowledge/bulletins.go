package knowledge

// The Bulletins section of the Knowledge page: boards whose latest post every
// following agent sees on each turn. The boards live in orchestrate
// (/orchestrate/api/bulletins); this only lays out the controls.

import "github.com/cmcoffee/gohort/core/ui"

const bulletinsAPI = "/orchestrate/api/bulletins"

func bulletinsSection() ui.Section {
	board := bulletinsAPI + "/{name}"
	// The post is what people come here to change, so it is the row's Edit,
	// in a dialog wide and tall enough to read a whole post at once.
	post := ui.ModalAction("Edit", ui.FormPanel{
		Source:      board,
		PostURL:     board + "/post",
		Method:      "POST",
		SubmitLabel: "Post",
		Invalidate:  []string{bulletinsAPI},
		Fields: []ui.FormField{{
			Field: "text", Label: "Post", Type: "textarea", Rows: 12, Required: true,
			Help: "Replaces the board's last post. At most 1,200 characters: it rides on every turn of every follower.",
		}},
	})
	post.Width = "720px"
	return ui.Section{
		Title:    "Bulletins",
		Subtitle: "Short notices every following agent sees on each turn.",
		Detail: "A collection is found by search when a question matches. A bulletin is pushed: a board holds one short latest post, and every agent that follows the board sees it on every turn, so a daily briefing or a message of the day is known without anyone fetching it. " +
			"Post yourself with Edit, empty it with Clear, change what it carries and how long a post stays current with Settings, let an agent post with Posters (it gets a bulletin_board tool that posts to and clears this board), and choose who reads it with Followers.",
		Body: ui.Stack{Children: []ui.Component{
			ui.ModalButton{
				Label:    "New board",
				Title:    "Make a board agents can follow",
				Subtitle: "Name it for what it carries, like news or motd.",
				Width:    "480px",
				Body: ui.FormPanel{
					PostURL:     bulletinsAPI,
					SubmitLabel: "Create",
					Invalidate:  []string{bulletinsAPI},
					Fields: []ui.FormField{
						{Field: "name", Label: "Name", Type: "text", Required: true, Placeholder: "news",
							Help: "Lowercase letters, digits, - and _."},
						{Field: "desc", Label: "What it carries", Type: "text", Placeholder: "Today's headlines, posted each morning"},
						{Field: "ttl_hours", Label: "Current for (hours)", Type: "number", Min: 0, Max: 8760,
							Help:   "How long a post stays current. 0 means it never goes out of date.",
							Detail: "Followers stop seeing a post once it is older than this, so yesterday's news never passes for today's. Leave 0 for a pinned notice such as a message of the day."},
					},
				},
			},
			ui.Table{
				Source:    bulletinsAPI,
				RowKey:    "name",
				EmptyText: "No boards yet. Make one, then choose who follows it and who may post to it.",
				// Each board is a card (bulletinCardsHead): the name and its
				// state on top, then what it carries, what was last done to it
				// and by whom, whether its followers see it, and its size,
				// followers and posters. bulletinCardsHead styles the second
				// line's cells by their place in this list.
				Columns: []ui.Col{
					{Field: "name"},
					{Field: "state", Type: "badge", Badges: []ui.BadgeMapping{
						{Value: "current", Label: "OK", Color: "success"},
						{Value: "stale", Label: "Out of date", Color: "warning"},
						{Value: "cleared", Label: "Cleared", Color: "mute"},
						{Value: "empty", Label: "Empty", Color: "mute"},
					}},
					{Field: "desc", Label: "What it carries", Line: 2, Mute: true},
					{Field: "posted", Label: "Last activity", Line: 2},
					{Field: "seen", Label: "Seen", Line: 2},
					{Field: "size", Label: "Size", Line: 2},
					{Field: "followers", Label: "Followers", Line: 2},
					{Field: "posters", Label: "Posters", Line: 2},
				},
				RowActions: []ui.RowAction{
					post,
					{Type: "button", Label: "Clear", Method: "POST", PostTo: board + "/clear", Compact: true, OnlyIf: "_has_post",
						Confirm: "Clear this board's post? Followers see nothing from it until the next post."},
					{Type: "button", Label: "Followers", Method: "client", PostTo: "bulletin_followers", Compact: true},
					{Type: "button", Label: "Posters", Method: "client", PostTo: "bulletin_posters", Compact: true},
					ui.ModalAction("Settings", ui.FormPanel{
						Source:      board,
						PostURL:     board,
						Method:      "POST",
						SubmitLabel: "Save",
						Invalidate:  []string{bulletinsAPI},
						Fields: []ui.FormField{
							{Field: "desc", Label: "What it carries", Type: "text"},
							{Field: "ttl_hours", Label: "Current for (hours)", Type: "number", Min: 0, Max: 8760,
								Help: "0 means a post never goes out of date."},
						},
					}),
					{Type: "button", Label: "Delete", Method: "DELETE", PostTo: board, Variant: "danger", Compact: true,
						Confirm: "Delete this board? Every agent stops following it, and its post is gone."},
				},
			},
		}},
	}
}

// bulletinCardsHead lays the boards out as cards like the collections above
// them, from this page alone: the Table is the framework's, so its buttons,
// dialogs and reloads stay its own, and only how its rows look changes here.
//
// The section is the one after the collections' card (the same hook the list's
// own styles use). A card is washed from the corner with its state, read off
// its pill: the accent while followers see the post, amber once it is out of
// date, nothing when the board is empty or cleared. The second line's cells
// come in the column order bulletinsSection declares: what it carries, the
// last activity, whether followers see it, then size, followers and posters,
// labelled here.
//
// The card says what a board is doing, not what its post says: the first
// lines of a post are as often a heading and a date as anything worth reading.
// The last-activity box opens the whole post (a click, or Enter once focused),
// with Edit beside it.
const bulletinCardsHead = `<style>
.ui-card:has(.docs-page) ~ .ui-section .ui-table-list {
  display: grid; grid-template-columns: repeat(auto-fill, minmax(min(340px, 100%), 1fr)); gap: 0.85rem;
}
.ui-card:has(.docs-page) ~ .ui-section .ui-table-row {
  --tone: var(--accent);
  flex-direction: column; flex-wrap: nowrap; align-items: stretch; gap: 0.75rem;
  padding: 1rem 1rem 0.85rem; border-radius: calc(var(--btn-radius) * 7 / 3);
  background: linear-gradient(155deg, color-mix(in srgb, var(--tone) 10%, var(--bg-2)) 0%, var(--bg-2) 60%);
}
.ui-card:has(.docs-page) ~ .ui-section .ui-table-row:has(.ui-badge.warning) { --tone: var(--warning); }
.ui-card:has(.docs-page) ~ .ui-section .ui-table-row:has(.ui-badge.mute) { --tone: var(--text-mute); background: var(--bg-2); }
.ui-card:has(.docs-page) ~ .ui-section .ui-row-lines { flex: none; gap: 0.6rem; }
.ui-card:has(.docs-page) ~ .ui-section .ui-row-lines > .ui-row-cells:first-child { display: flex; align-items: flex-start; gap: 0.6rem; }
.ui-card:has(.docs-page) ~ .ui-section .ui-table-cell-key {
  flex: 1 1 auto; min-width: 0; margin: 0;
  font-weight: 600; color: var(--text-hi); font-size: var(--fs-lg, 1rem); line-height: 1.25;
  white-space: normal; overflow-wrap: anywhere;
}
.ui-card:has(.docs-page) ~ .ui-section .ui-row-cells > .ui-table-cell-badge { margin: 0 0 0 auto; flex-shrink: 0; }
.ui-card:has(.docs-page) ~ .ui-section .ui-row-cells-2 {
  display: flex; flex-direction: column; align-items: stretch; gap: 0.45rem; opacity: 1;
}
.ui-card:has(.docs-page) ~ .ui-section .ui-row-cells-2 > .ui-table-cell {
  display: block; overflow: visible; white-space: normal; overflow-wrap: anywhere; margin: 0;
  -webkit-line-clamp: none; line-clamp: none;
  font-size: var(--fs-sm, 0.84rem); line-height: 1.45; color: var(--text);
}
.ui-card:has(.docs-page) ~ .ui-section .ui-row-cells-2 > .ui-table-cell:empty { display: none; }
/* What it carries: tucked under the name. */
.ui-card:has(.docs-page) ~ .ui-section .ui-row-cells-2 > .ui-table-cell:nth-child(1) {
  margin-top: -0.45rem; color: var(--text-mute); font-size: var(--fs-xs, 0.78rem);
}
/* The last activity, in a box edged with the card's state. */
.ui-card:has(.docs-page) ~ .ui-section .ui-row-cells-2 > .ui-table-cell:nth-child(2) {
  display: flex; align-items: baseline; gap: 0.75rem;
  margin: 0.15rem 0 0.1rem; padding: 0.55rem 0.75rem;
  box-shadow: inset 3px 0 0 color-mix(in srgb, var(--tone) 70%, transparent);
  border-radius: 0 var(--btn-radius) var(--btn-radius) 0;
  background: color-mix(in srgb, var(--tone) 6%, var(--bg-0));
  font-size: var(--fs-md, 0.9rem); font-weight: 500;
}
.ui-card:has(.docs-page) ~ .ui-section .ui-row-cells-2 > .ui-table-cell:nth-child(2)[data-read] { cursor: pointer; }
.ui-card:has(.docs-page) ~ .ui-section .ui-row-cells-2 > .ui-table-cell:nth-child(2)[data-read]::after {
  content: "Read \203a"; margin-left: auto; white-space: nowrap;
  color: var(--accent); font-size: var(--fs-sm, 0.84rem); font-weight: 500;
}
.ui-card:has(.docs-page) ~ .ui-section .ui-row-cells-2 > .ui-table-cell:nth-child(2)[data-read]:hover {
  background: color-mix(in srgb, var(--tone) 14%, var(--bg-0));
}
.ui-card:has(.docs-page) ~ .ui-section .ui-row-cells-2 > .ui-table-cell:nth-child(2):focus-visible {
  outline: 2px solid var(--accent); outline-offset: 2px;
}
/* Size, followers and posters: labelled rows. */
.ui-card:has(.docs-page) ~ .ui-section .ui-row-cells-2 > .ui-table-cell:nth-child(n+4) {
  display: grid; grid-template-columns: 5.5rem 1fr; gap: 0.75rem; font-size: var(--fs-xs, 0.8rem);
}
.ui-card:has(.docs-page) ~ .ui-section .ui-row-cells-2 > .ui-table-cell:nth-child(n+4)::before { color: var(--text-mute); }
.ui-card:has(.docs-page) ~ .ui-section .ui-row-cells-2 > .ui-table-cell:nth-child(4)::before { content: "Size"; }
.ui-card:has(.docs-page) ~ .ui-section .ui-row-cells-2 > .ui-table-cell:nth-child(5)::before { content: "Followers"; }
.ui-card:has(.docs-page) ~ .ui-section .ui-row-cells-2 > .ui-table-cell:nth-child(6)::before { content: "Posters"; }
.ui-card:has(.docs-page) ~ .ui-section .ui-row-cells-2 > .ui-table-cell:nth-child(4) {
  margin-top: 0.15rem; padding-top: 0.55rem; border-top: 1px dashed color-mix(in srgb, var(--border) 80%, transparent);
}
/* As specific as the rules above it, or a labelled row with no value (an
   empty board's size) still showed its label. */
.ui-card:has(.docs-page) ~ .ui-section .ui-row-cells-2 > .ui-table-cell:nth-child(n+1):empty { display: none; }
/* No size on an empty board, so the rule moves to the followers' row. */
.ui-card:has(.docs-page) ~ .ui-section .ui-row-cells-2 > .ui-table-cell:nth-child(4):empty + .ui-table-cell {
  margin-top: 0.15rem; padding-top: 0.55rem; border-top: 1px dashed color-mix(in srgb, var(--border) 80%, transparent);
}
.bulletin-read {
  white-space: pre-wrap; overflow-wrap: anywhere; line-height: 1.55;
  font-size: var(--fs-md, 0.9rem); color: var(--text);
}
.bulletin-read.mute { color: var(--text-mute); font-style: italic; }
.ui-card:has(.docs-page) ~ .ui-section .ui-row-actions {
  margin: auto 0 0; padding-top: 0.75rem; width: 100%; box-sizing: border-box;
  border-top: 1px solid color-mix(in srgb, var(--border) 70%, transparent);
  justify-content: flex-start; gap: 0.4rem;
}
.ui-card:has(.docs-page) ~ .ui-section .ui-row-actions > .danger { margin-left: auto; }
</style>
<script>
(function(){
  var activity = '.ui-card:has(.docs-page) ~ .ui-section .ui-row-cells-2 > .ui-table-cell:nth-child(2)';
  function cellText(row, n){
    var c = row.querySelector('.ui-row-cells-2 > .ui-table-cell:nth-child(' + n + ')');
    return c ? c.textContent.trim() : '';
  }
  // The rows are redrawn on every reload, so each new activity box is made a
  // control as it arrives: focusable, and said to be one. Only a board with a
  // post (it has a size) has anything to read.
  function mark(){
    document.querySelectorAll(activity + ':not([data-checked])').forEach(function(c){
      c.setAttribute('data-checked', '1');
      var row = c.closest('.ui-table-row');
      if (!row || !cellText(row, 4)) return;
      c.setAttribute('data-read', '1');
      c.setAttribute('role', 'button');
      c.tabIndex = 0;
      c.title = 'Read the whole post';
    });
  }
  function read(cell){
    var row = cell.closest('.ui-table-row');
    var key = row && row.querySelector('.ui-table-cell-key');
    var name = key ? key.textContent.trim() : '';
    if (!name || !window.uiOpenSimpleModal) return;
    window.uiOpenSimpleModal({
      title: name,
      subtitle: [cellText(row, 2), cellText(row, 4)].filter(Boolean).join(' \u00b7 '),
      width: '640px',
      mount: function(body, dlg){
        var text = document.createElement('div');
        text.className = 'bulletin-read mute';
        text.textContent = 'Loading the post';
        body.appendChild(text);
        fetch('` + bulletinsAPI + `/' + encodeURIComponent(name), {cache: 'no-store'}).then(function(res){
          if (!res.ok) return res.text().then(function(t){ throw new Error(t || ('HTTP ' + res.status)); });
          return res.json();
        }).then(function(b){
          var t = (b && b.text) || '';
          text.className = 'bulletin-read' + (t ? '' : ' mute');
          text.textContent = t || 'Nothing is posted on this board.';
        }).catch(function(e){
          text.textContent = 'Could not load the post: ' + e.message;
        });
        // Edit is the row's own, so the post is changed in the one dialog
        // that does it.
        var edit = document.createElement('button');
        edit.className = 'ui-row-btn';
        edit.textContent = 'Edit';
        edit.onclick = function(){
          dlg.close();
          var btns = row.querySelectorAll('.ui-row-actions button');
          for (var i = 0; i < btns.length; i++) {
            if (btns[i].textContent.trim() === 'Edit') { btns[i].click(); return; }
          }
        };
        dlg.addAction(edit);
      }
    });
  }
  document.addEventListener('click', function(e){
    var c = e.target.closest && e.target.closest(activity + '[data-read]');
    if (c) read(c);
  });
  document.addEventListener('keydown', function(e){
    if (e.key !== 'Enter' && e.key !== ' ') return;
    var c = e.target.closest && e.target.closest(activity + '[data-read]');
    if (!c) return;
    e.preventDefault();
    read(c);
  });
  function watch(){
    mark();
    new MutationObserver(mark).observe(document.body, {childList: true, subtree: true});
  }
  if (document.body) watch(); else document.addEventListener('DOMContentLoaded', watch);
})();
</script>`

// bulletinPillsHead registers the Followers and Posters row actions: the
// framework's pill renderer (uiRenderScopePills) pointed at the board's
// endpoint. The table reloads after each change so its columns stay true.
const bulletinPillsHead = `<script>
(function(){
  function register(){
    if (!window.uiRegisterClientAction || !window.uiOpenSimpleModal) { setTimeout(register, 50); return; }
    function open(which, title){
      return function(ctx){
        var r = (ctx && ctx.record) || {};
        if (!r.name) return;
        var base = '` + bulletinsAPI + `/' + encodeURIComponent(r.name) + '/' + which;
        var reload = ctx && ctx.reload;
        window.uiOpenSimpleModal({
          title: title + ': ' + r.name,
          width: '560px',
          mount: function(body){
            var host = document.createElement('div');
            body.appendChild(host);
            window.uiRenderScopePills(host, {
              load: function(){
                return fetch(base + '?pills=1', {cache:'no-store'}).then(function(res){
                  if (!res.ok) return res.text().then(function(t){ throw new Error(t || ('HTTP ' + res.status)); });
                  return res.json();
                });
              },
              toggle: function(key, on){
                return fetch(base, {method:'POST', headers:{'Content-Type':'application/json'},
                  body: JSON.stringify({target: key, on: on})}).then(function(res){
                  if (!res.ok) return res.text().then(function(t){ throw new Error(t || ('HTTP ' + res.status)); });
                  if (reload) reload();
                });
              }
            });
          }
        });
      };
    }
    window.uiRegisterClientAction('bulletin_followers', open('followers', 'Followers'));
    window.uiRegisterClientAction('bulletin_posters', open('posters', 'Posters'));
  }
  register();
})();
</script>`
