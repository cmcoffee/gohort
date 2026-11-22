// An app from an MCP client, on its Settings page.
//
// An app that offers something beyond its page (exposed_tools.go) is an MCP
// server of its own at /mcp/app/<slug>/ (apps/mcpserver/app_server.go). Its
// owner finds the address here with what it offers, makes a key for it, and
// sees and revokes the keys that name it. A key made here reaches this app and
// nothing else (AppToolsTarget): no agent, no built-in tool, no other app.
// The app's own agent is never reached from outside, except through an
// action of the app's that uses it.
//
// The owner may offer it to the people the app is shared with too
// (OfferMCPToShared). Each of them then finds the same section on their own
// Settings for the app, and makes a key of their own, pinned to the owner's
// app; a call runs as their own click would, on their own records. The owner
// sees whose keys name the app and when each was last used, not what they
// did. Turning it off stops them all at once; unsharing stops that person.
package customapps

import (
	"html"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/netgate"
	"github.com/cmcoffee/gohort/core/ui"
	"github.com/cmcoffee/gohort/core/userprofile"
)

// appMCPKeyAction is the client action that shows a key made here, once.
const appMCPKeyAction = "app_mcp_key"

// appMCPPath is an app's MCP address on this site.
func appMCPPath(slug string) string { return "/mcp/app/" + slug + "/" }

// appMCPAddress is an app's MCP address as a client is given it: this site's
// scheme and host, as the request reached it.
func appMCPAddress(r *http.Request, slug string) string {
	scheme := "http"
	if TLSEnabled() || (r != nil && (r.TLS != nil || netgate.ForwardedHTTPS(r))) {
		scheme = "https"
	}
	host := ""
	if r != nil {
		host = r.Host
	}
	return scheme + "://" + host + appMCPPath(slug)
}

// mcpOpenTo reports whether an administrator opened MCP to user.
func mcpOpenTo(user string) bool {
	for _, f := range KeyKindDoors(KeyKindMCP) {
		if !FeatureAllowedForUser(RootDB, f, user) {
			return false
		}
	}
	return true
}

// offeringApp is the app owner's slug as user may use it over MCP, when they
// may: their own, or one shared with them and offered to them.
func (T *CustomApps) offeringApp(user, owner, slug string) (OfferingApp, bool) {
	for _, a := range T.offeringApps(user) {
		if a.Slug == slug && a.Owner == owner {
			return a, true
		}
	}
	return OfferingApp{}, false
}

// callerTarget is the key target that names the app for the caller: its
// slug for the owner, pinned to the owner for anyone else.
func callerTarget(a *appCall) string {
	if a.mine() {
		return AppToolsTarget + a.spec.Slug
	}
	return AppToolsTarget + a.owner + "/" + a.spec.Slug
}

// mcpSection is the Settings section for using the app from an MCP client:
// the owner's, and a shared person's once the owner offers it to them.
func (T *CustomApps) mcpSection(r *http.Request, spec AppSpec, user, owner, base string) *ui.Section {
	app, ok := T.offeringApp(user, owner, spec.Slug)
	if user != owner && !ok {
		return nil
	}
	sec := &ui.Section{Title: "Use from an MCP client"}
	switch {
	case !ok:
		sec.Subtitle = "This app offers nothing beyond its page yet, so an MCP client would find nothing to use. Builder or Anvil can mark an action, a data source or its pipeline as offered."
		return sec
	case !mcpOpenTo(user):
		sec.Subtitle = "An MCP client could use what this app offers, but an administrator has not opened MCP to your account (Feature Access)."
		return sec
	}
	var offered, asks []string
	for _, d := range app.Tools {
		if d.Confirmation != nil {
			asks = append(asks, d.Tool.Name)
		} else {
			offered = append(offered, d.Tool.Name)
		}
	}
	var card strings.Builder
	card.WriteString("<p><b>Address</b> <code>" + html.EscapeString(appMCPAddress(r, spec.Slug)) + "</code></p>")
	if len(offered) > 0 {
		card.WriteString("<p><b>Offers</b> " + html.EscapeString(strings.Join(offered, ", ")) + "</p>")
	}
	if len(asks) > 0 {
		card.WriteString("<p><b>Asks first</b> " + html.EscapeString(strings.Join(asks, ", ")) + ": each asks before it runs. A client lists these only on a key set to ask in the client, and only if it can ask you mid-call (Claude Code and Claude Desktop can): each call then shows you the app's question and runs only on your yes. Other clients do not see them.</p>")
	}
	if user == owner {
		sec.Subtitle = "An MCP client such as Claude Desktop can use this app on its own, with no page open. At this address it finds what the app offers, and a call does what your click would. The app's agent is not reached from there."
	} else {
		sec.Subtitle = owner + " offers this app to the people it is shared with, from an MCP client such as Claude Desktop. At this address your key finds what the app offers, and a call does what your click would, on your own records. The app's agent is not reached from there."
	}
	keys := base + "/_settings/mcp/keys"
	fields := []ui.FormField{{Field: "client", Label: "Used by", Type: "text",
		Help: "What will use the key, so you can tell keys apart later. The key reaches this app and nothing else."}}
	cols := []ui.Col{
		{Field: "name", Label: "Key", Flex: 2},
		{Field: "reach", Label: "Reaches", Flex: 2, Mute: true},
		{Field: "last_seen", Label: "Last used", Format: "reltime", Mute: true},
		{Field: "expires", Label: "Expires", Format: "reltime", Mute: true},
	}
	var rowActs []ui.RowAction
	if len(asks) > 0 {
		fields = append(fields, ui.FormField{Field: "asks", Label: "Ask in the client", Type: "toggle", Help: askInClientHelp})
		cols = append(cols, ui.Col{Field: "unasked", Label: "Runs unasked", Flex: 2, Mute: true})
		rowActs = append(rowActs,
			ui.RowAction{Type: "toggle", Field: "asks", Label: "Ask in the client", PostTo: keys + "/{id}/ask"},
			ui.RowAction{Type: "button", Label: "Ask again", PostTo: keys + "/{id}/forget", OnlyIf: "unasked",
				Confirm: "Ask again before every call of the actions this key runs unasked?"})
	}
	rowActs = append(rowActs, ui.RowAction{Type: "button", Label: "Revoke", Method: "DELETE",
		PostTo: keys + "/{id}", Variant: "danger",
		Confirm: "Revoke this key? A client using it stops working at once."})
	children := []ui.Component{
		ui.Card{HTML: card.String()},
		ui.FormPanel{
			Source:      base + "/_settings/mcp/mint",
			Method:      "POST",
			SubmitLabel: "Make a key for this app",
			OnSuccess:   appMCPKeyAction,
			Invalidate:  []string{keys},
			Fields:      fields,
		},
		ui.Table{
			Source:     keys,
			RowKey:     "id",
			EmptyText:  "No key of yours names this app yet.",
			Columns:    cols,
			RowActions: rowActs,
		},
	}
	if user == owner && appSharedAnyWay(spec) {
		people := base + "/_settings/mcp/people"
		children = append(children,
			ui.FormPanel{
				Source:     base + "/_settings/mcp/offer",
				Invalidate: []string{people},
				Fields: []ui.FormField{{Field: "offer_to_shared", Label: "Offer to people it's shared with", Type: "toggle",
					Help: "Each of them can make a key of their own and use the app from an MCP client, on their own records. Every call runs the app's scripts as you, with your connections, and a client can call far more often than a person clicks. Off stops them all at once."}},
			},
			ui.Table{
				Source:    people,
				RowKey:    "id",
				EmptyText: "Nobody it is shared with has a key for it.",
				Columns: []ui.Col{
					{Field: "person", Label: "Person", Flex: 2},
					{Field: "name", Label: "Key", Flex: 2, Mute: true},
					{Field: "last_seen", Label: "Last used", Format: "reltime", Mute: true},
				},
			})
	}
	sec.Body = ui.Stack{Children: children}
	return sec
}

// askInClientHelp is the help on the key's asking toggle.
const askInClientHelp = "Lets a client that can ask you mid-call (Claude Code, Claude Desktop) use the actions that confirm first: before each call it shows you the app's question, and the action runs only on your yes. You can tick \"Don't ask again\" there, for this key and that action alone. Leave it off for a client that runs on its own, which could answer yes itself."

// mcpRoutes are the section's endpoints.
func (T *CustomApps) mcpRoutes() {
	T.HandleFunc("/{slug}/_settings/mcp/mint", jsonapi.Methods{
		"GET":  T.appGet(func(*appCall) (any, error) { return map[string]any{"client": "", "asks": false}, nil }),
		"POST": appPost(T, T.mintAppMCPKey),
	}.Serve)
	T.HandleFunc("/{slug}/_settings/mcp/keys", jsonapi.Methods{"GET": T.appGet(T.appMCPKeys)}.Serve)
	T.HandleFunc("/{slug}/_settings/mcp/keys/{id}", appDelete(T, T.revokeAppMCPKey))
	T.HandleFunc("/{slug}/_settings/mcp/keys/{id}/ask", appPost(T, T.setAppMCPKeyAsks))
	T.HandleFunc("/{slug}/_settings/mcp/keys/{id}/forget", appPost(T, T.forgetAppMCPKeyYeses))
	T.HandleFunc("/{slug}/_settings/mcp/offer", jsonapi.Methods{
		"GET":  T.appGet(T.appMCPOffer),
		"POST": appPost(T, T.saveAppMCPOffer),
	}.Serve)
	T.HandleFunc("/{slug}/_settings/mcp/people", jsonapi.Methods{"GET": T.appGet(T.appMCPPeople)}.Serve)
}

// mcpReady is the app as the caller may make a key for it: their own,
// offering something, or shared with them and offered to them; with MCP open
// to them.
func (T *CustomApps) mcpReady(a *appCall) (OfferingApp, error) {
	app, ok := T.offeringApp(a.User, a.owner, a.spec.Slug)
	switch {
	case !ok && a.mine():
		return OfferingApp{}, jsonapi.BadRequest("this app offers nothing beyond its page yet: mark an action, a data source or its pipeline as offered first")
	case !ok:
		return OfferingApp{}, jsonapi.Forbidden("the app's owner does not offer it to you over MCP")
	case !mcpOpenTo(a.User):
		return OfferingApp{}, jsonapi.Forbidden("an administrator has not opened MCP to your account (Feature Access)")
	}
	return app, nil
}

// mintAppMCPKey makes the caller an MCP key that reaches this app alone and
// answers it, the one time it is shown, with the address and a name for the
// server.
func (T *CustomApps) mintAppMCPKey(a *appCall, in struct {
	Client string `json:"client"`
	Asks   bool   `json:"asks"`
}) (any, error) {
	app, err := T.mcpReady(a)
	if err != nil {
		return nil, err
	}
	// A key made here is a personal API key like any other: one the person's
	// user profile takes away would be made and then refused on every use.
	if userprofile.TakesFrom(a.User, userprofile.RemoveAPIKeys) {
		return nil, jsonapi.Forbidden(userprofile.Refusal)
	}
	name := app.Name
	if c := strings.TrimSpace(in.Client); c != "" {
		name = c + " for " + app.Name
	}
	tok := MintAccountTokenScoped(a.User, name, &TokenScope{
		Features: KeyKindDoors(KeyKindMCP),
		Targets:  []string{app.Target()},
	})
	if in.Asks {
		SetMCPKeyAsks(tok.ID, true)
	}
	Log("[customapps] %s made MCP key %q for app %s/%s (asks in the client: %v)", a.User, name, a.owner, a.spec.Slug, in.Asks)
	return map[string]any{
		"ok":      true,
		"key":     tok.Token,
		"name":    name,
		"server":  a.spec.Slug,
		"address": appMCPAddress(a.R, a.spec.Slug),
		"message": "Key made. This is the only time it is shown.",
	}, nil
}

// namesTarget reports whether tok names target among what it reaches.
func namesTarget(tok AccountToken, target string) bool {
	if tok.Scope == nil {
		return false
	}
	for _, t := range tok.Scope.Targets {
		if strings.EqualFold(strings.TrimSpace(t), target) {
			return true
		}
	}
	return false
}

// appMCPKeys are the caller's keys that name this app: made here, or ticked
// for it on the Account page. Listed whether or not the app is still offered
// to them, so a key can always be revoked.
func (T *CustomApps) appMCPKeys(a *appCall) (any, error) {
	target := callerTarget(a)
	rows := []map[string]any{}
	for _, tok := range ListAccountTokens(a.User) {
		if !namesTarget(tok, target) {
			continue
		}
		reach := "this app alone"
		if n := len(tok.Scope.Targets); n > 1 {
			reach = "this app and " + plural(n-1, "other thing")
		}
		rows = append(rows, map[string]any{
			"id": tok.ID, "name": tok.Name, "reach": reach,
			"last_seen": tok.LastSeen, "expires": tok.Expires,
			"asks": MCPKeyAsks(tok.ID), "unasked": strings.Join(unaskedNames(tok.ID), ", "),
		})
	}
	return rows, nil
}

// revokeAppMCPKey revokes one of the caller's keys that names this app.
func (T *CustomApps) revokeAppMCPKey(a *appCall, _ struct{}) (any, error) {
	id, target := a.R.PathValue("id"), callerTarget(a)
	for _, tok := range ListAccountTokens(a.User) {
		if tok.ID == id && namesTarget(tok, target) {
			if !RevokeAccountToken(a.User, id) {
				return nil, jsonapi.NotFound("no such key")
			}
			SetMCPKeyAsks(id, false)
			Log("[customapps] %s revoked MCP key %q of app %s/%s", a.User, tok.Name, a.owner, a.spec.Slug)
			return map[string]any{"ok": true, "message": "Key revoked."}, nil
		}
	}
	return nil, jsonapi.NotFound("no key of yours for this app by that id")
}

// callerKey is the caller's key by the path's id when it names this app.
func callerKey(a *appCall) (AccountToken, error) {
	id, target := a.R.PathValue("id"), callerTarget(a)
	for _, tok := range ListAccountTokens(a.User) {
		if tok.ID == id && namesTarget(tok, target) {
			return tok, nil
		}
	}
	return AccountToken{}, jsonapi.NotFound("no key of yours for this app by that id")
}

// setAppMCPKeyAsks turns asking in the client on or off for one of the
// caller's keys that names this app. Off forgets the yeses it kept.
func (T *CustomApps) setAppMCPKeyAsks(a *appCall, in struct {
	Asks bool `json:"asks"`
}) (any, error) {
	tok, err := callerKey(a)
	if err != nil {
		return nil, err
	}
	SetMCPKeyAsks(tok.ID, in.Asks)
	Log("[customapps] %s set MCP key %q of app %s/%s to ask in the client: %v", a.User, tok.Name, a.owner, a.spec.Slug, in.Asks)
	return map[string]any{"ok": true, "asks": in.Asks}, nil
}

// forgetAppMCPKeyYeses makes one of the caller's keys ask again before every
// action it ran unasked.
func (T *CustomApps) forgetAppMCPKeyYeses(a *appCall, _ struct{}) (any, error) {
	tok, err := callerKey(a)
	if err != nil {
		return nil, err
	}
	forgetMCPYeses(tok.ID)
	Log("[customapps] %s made MCP key %q of app %s/%s ask again", a.User, tok.Name, a.owner, a.spec.Slug)
	return map[string]any{"ok": true, "message": "It asks again before every call."}, nil
}

// appMCPOffer is whether the owner offers the app over MCP to the people it
// is shared with.
func (T *CustomApps) appMCPOffer(a *appCall) (any, error) {
	if !a.mine() {
		return nil, jsonapi.Forbidden("only the app's owner offers it")
	}
	return map[string]bool{"offer_to_shared": a.spec.OfferMCPToShared}, nil
}

// saveAppMCPOffer turns the offer on or off. It is how this copy runs here,
// not part of the app, so it is saved without a version in its history.
func (T *CustomApps) saveAppMCPOffer(a *appCall, in struct {
	Offer bool `json:"offer_to_shared"`
}) (any, error) {
	if !a.mine() {
		return nil, jsonapi.Forbidden("only the app's owner offers it")
	}
	spec, ok := loadSpec(a.owner, a.spec.Slug)
	if !ok {
		return nil, errNoApp
	}
	spec.OfferMCPToShared = in.Offer
	SaveAppSpecBy(spec, AppSaveNoHistory, a.owner, "")
	Log("[customapps] %s set app %s offered over MCP to the people it is shared with: %v", a.owner, spec.Slug, in.Offer)
	return map[string]bool{"offer_to_shared": in.Offer}, nil
}

// appMCPPeople are the people other than the owner whose keys name the app,
// with when each key was last used: who has connected, not what they did.
func (T *CustomApps) appMCPPeople(a *appCall) (any, error) {
	if !a.mine() {
		return nil, jsonapi.Forbidden("only the app's owner sees who has connected")
	}
	target := AppToolsTarget + a.owner + "/" + a.spec.Slug
	rows := []map[string]any{}
	if AuthDB == nil || AuthDB() == nil {
		return rows, nil
	}
	// The owner's own keys name the app by its slug alone, so none of
	// theirs is listed here.
	for _, u := range AuthListUsers(AuthDB()) {
		for _, tok := range ListAccountTokens(u.Username) {
			if namesTarget(tok, target) {
				rows = append(rows, map[string]any{"id": tok.ID, "person": u.Username, "name": tok.Name, "last_seen": tok.LastSeen})
			}
		}
	}
	return rows, nil
}

// appMCPKeyJS shows a key made for an app, once, with the address and a
// client config to paste: Copy key and Copy config in the footer beside Done.
const appMCPKeyJS = `function(ctx){
  var r = (ctx && ctx.response) || {};
  if (!r.key) return;
  var servers = {};
  servers[r.server || 'app'] = {type: 'http', url: r.address, headers: {'X-API-Key': r.key}};
  var cfg = JSON.stringify({mcpServers: servers}, null, 2);
  function copy(text, btn) {
    if (navigator.clipboard) navigator.clipboard.writeText(text);
    var was = btn.firstChild ? btn.firstChild.nodeValue : '';
    if (btn.firstChild) btn.firstChild.nodeValue = 'Copied';
    setTimeout(function(){ if (btn.firstChild) btn.firstChild.nodeValue = was; }, 1400);
  }
  function block(text) {
    var pre = document.createElement('pre');
    pre.textContent = text;
    pre.style.cssText = 'margin:0;white-space:pre-wrap;word-break:break-all;font-family:ui-monospace,SFMono-Regular,Menlo,monospace;' +
      'font-size:var(--fs-sm, 0.82rem);padding:0.6rem 0.7rem;border:1px solid var(--border);border-radius:8px;background:var(--bg-2)';
    return pre;
  }
  window.uiOpenModal({
    title: 'Key for ' + (r.name || 'this app'),
    subtitle: 'Copy it now: this is the only time it is shown. It reaches this app and nothing else. If it is lost, revoke it and make another.',
    width: '640px',
    actions: [
      {label: 'Copy key', onClick: function(api, btn){ copy(r.key, btn); }},
      {label: 'Copy config', onClick: function(api, btn){ copy(cfg, btn); }},
      {label: 'Done', primary: true}
    ],
    mount: function(box) {
      var wrap = document.createElement('div');
      wrap.style.cssText = 'display:flex;flex-direction:column;gap:0.6rem';
      wrap.appendChild(block(r.key));
      var note = document.createElement('div');
      note.textContent = 'For a client that takes a server list (Claude Desktop, Claude Code and others), the app as a server:';
      wrap.appendChild(note);
      wrap.appendChild(block(cfg));
      box.appendChild(wrap);
    }
  });
}`
