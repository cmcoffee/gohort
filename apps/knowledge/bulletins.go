package knowledge

// The Bulletins section of the Knowledge page: boards whose latest post every
// following agent sees on each turn. The boards live in orchestrate
// (/orchestrate/api/bulletins); this only lays out the controls.

import "github.com/cmcoffee/oddjob/core/ui"

const bulletinsAPI = "/orchestrate/api/bulletins"

func bulletinsSection() ui.Section {
	board := bulletinsAPI + "/{name}"
	return ui.Section{
		Title:    "Bulletins",
		Subtitle: "Short notices every following agent sees on each turn.",
		Detail: "A collection is found by search when a question matches. A bulletin is pushed: a board holds one short latest post, and every agent that follows the board sees it on every turn, so a daily briefing or a message of the day is known without anyone fetching it. " +
			"Post yourself with Write post, let an agent post with Posters (it gets a post_bulletin tool for this board), and choose who reads it with Followers.",
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
				Columns: []ui.Col{
					{Field: "name"},
					{Field: "latest", Label: "Latest post", Flex: 3},
					{Field: "posted", Label: "Posted", Mute: true, Flex: 2},
					{Field: "followers", Label: "Followed by", Mute: true, Flex: 2},
				},
				RowActions: []ui.RowAction{
					ui.ModalAction("Write post", ui.FormPanel{
						Source:      board,
						PostURL:     board + "/post",
						Method:      "POST",
						SubmitLabel: "Post",
						Invalidate:  []string{bulletinsAPI},
						Fields: []ui.FormField{{
							Field: "text", Label: "Post", Type: "textarea", Rows: 5, Required: true,
							Help: "Replaces the board's last post. At most 600 characters: it rides on every turn of every follower.",
						}},
					}),
					{Type: "button", Label: "Followers", Method: "client", PostTo: "bulletin_followers", Compact: true},
					{Type: "button", Label: "Posters", Method: "client", PostTo: "bulletin_posters", Compact: true},
					ui.ModalAction("Edit", ui.FormPanel{
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
