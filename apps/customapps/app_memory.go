package customapps

// What an app remembers about the person using it, on its Settings page (the
// ⚙ in its bar): what its agents know from the start, read-only; what they
// learned about this person, each to forget, or all of it; and a switch to
// stop it remembering them. Each person sees only their own, read from their
// own store: the app's memory of someone else is never in reach
// (docs/design/app-history.md, piece E; orchestrate's app_memory.go keeps it).

import (
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/agenthost"
	"github.com/cmcoffee/gohort/core/applocal"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/ui"
)

// memoryRoutes serves the Settings page's memory section.
func (T *CustomApps) memoryRoutes() {
	T.HandleFunc("/{slug}/_memory", jsonapi.Methods{"GET": T.appGet(T.appMemoryList)}.Serve)
	T.HandleFunc("/{slug}/_memory/forget", appPost(T, T.appMemoryForget))
	T.HandleFunc("/{slug}/_memory/me", jsonapi.Methods{
		"GET":  T.appGet(T.appMemoryMe),
		"POST": appPost(T, T.saveAppMemoryMe),
	}.Serve)
	T.HandleFunc("/{slug}/_settings/curation", jsonapi.Methods{
		"GET":  T.appGet(T.appCuration),
		"POST": appPost(T, T.saveAppCuration),
	}.Serve)
}

// personStore is the store of the person making a call, where what an app
// remembers about them is kept.
func personStore(user string) Database {
	h := agenthost.Get()
	if h == nil || user == "" {
		return nil
	}
	return h.MachineStore(user)
}

// appKeepsMemory reports whether the app remembers the people using it.
func appKeepsMemory(spec AppSpec) bool { return spec.Memory == "on" }

// appMemoryList is what the app knows from the start and what it remembers
// about the caller.
func (T *CustomApps) appMemoryList(a *appCall) (any, error) {
	starting := []map[string]any{}
	for _, f := range a.spec.StartingFacts {
		starting = append(starting, map[string]any{"note": f})
	}
	learned := []map[string]any{}
	if appKeepsMemory(a.spec) {
		for _, f := range ListMemoryFacts(personStore(a.User), applocal.AppMemoryNamespace(a.owner, a.spec.Slug)) {
			learned = append(learned, map[string]any{"id": f.ID, "note": f.Note, "created": f.Created})
		}
	}
	return map[string]any{"starting": starting, "facts": learned}, nil
}

// appMemoryForget forgets one thing the app remembers about the caller
// (?id=), or all of it.
func (T *CustomApps) appMemoryForget(a *appCall, _ struct{}) (any, error) {
	db := personStore(a.User)
	ns := applocal.AppMemoryNamespace(a.owner, a.spec.Slug)
	id := strings.TrimSpace(a.R.URL.Query().Get("id"))
	n := 0
	for _, f := range ListMemoryFacts(db, ns) {
		if (id == "" || f.ID == id) && ForgetMemoryFactByID(db, ns, f.ID) {
			n++
		}
	}
	if id != "" && n == 0 {
		return nil, jsonapi.BadRequest("nothing to forget by that id")
	}
	return map[string]any{"ok": true, "forgotten": n}, nil
}

// appMemoryMe is the caller's own switch: whether the app remembers them.
func (T *CustomApps) appMemoryMe(a *appCall) (any, error) {
	return map[string]any{"remember": !applocal.AppMemoryOptedOut(personStore(a.User), a.owner, a.spec.Slug)}, nil
}

func (T *CustomApps) saveAppMemoryMe(a *appCall, in struct {
	Remember bool `json:"remember"`
}) (any, error) {
	applocal.SetAppMemoryOptOut(personStore(a.User), a.owner, a.spec.Slug, !in.Remember)
	return map[string]any{"ok": true}, nil
}

// memorySection is the Settings page's "What it remembers" section, for an
// app that keeps a memory; nil for one that does not.
func memorySection(spec AppSpec, base string) *ui.Section {
	if !appKeepsMemory(spec) {
		return nil
	}
	var parts []ui.Component
	if len(spec.StartingFacts) > 0 {
		parts = append(parts, ui.Table{
			Source: base + "/_memory", RecordsField: "starting", RowKey: "note",
			Columns: []ui.Col{{Field: "note", Label: "What it knows from the start", Flex: 1}},
		})
	}
	parts = append(parts,
		ui.Table{
			Source: base + "/_memory", RecordsField: "facts", RowKey: "id",
			Columns: []ui.Col{
				{Field: "note", Label: "What it learned about you", Flex: 1},
				{Field: "created", Format: "reltime", Mute: true},
			},
			RowActions: []ui.RowAction{{Type: "button", Label: "Forget", PostTo: base + "/_memory/forget?id={id}",
				Confirm: "Forget this? The app's agents will not know it any more."}},
			EmptyText: "It remembers nothing about you.",
		},
		ui.Toolbar{Actions: []ui.ToolbarAction{{
			Label: "Forget everything", Method: "POST", Variant: "danger", URL: base + "/_memory/forget",
			Confirm:     "Forget everything this app remembers about you? Its agents start over with you.",
			RedirectURL: base + "/_settings",
		}}},
		ui.FormPanel{
			Source: base + "/_memory/me",
			Fields: []ui.FormField{{Field: "remember", Label: "Remember me in this app", Type: "toggle",
				Help: "Off: nothing new is kept about you. What is kept stays until you forget it."}},
		},
	)
	return &ui.Section{
		Title:    "What it remembers",
		Subtitle: "What this app's agents remember about you. Only you see this: what it remembers about anyone else is theirs.",
		Body:     ui.Stack{Children: parts},
	}
}

// changeSection is the way to change the app, for those who may: its owner
// and its maintainers. Nil for anyone else.
func changeSection(spec AppSpec, user, owner string) *ui.Section {
	if user != owner && !spec.MaintainedBy(user) {
		return nil
	}
	link := builderChangeURL(spec.Slug)
	if link == "" {
		return nil
	}
	return &ui.Section{
		Title:    "Change it",
		Subtitle: "Builder changes the app for you: its page, scripts, agents and tools, with its checks and history.",
		Body:     ui.Toolbar{Actions: []ui.ToolbarAction{{Label: "Change with Builder", Method: "GET", URL: link}}},
	}
}

// hasSettingsPage reports whether the ⚙ has anything to show user: settings
// they may set, a memory the app keeps, or a way to change it.
func hasSettingsPage(spec AppSpec, user, owner string) bool {
	return len(visibleSettings(spec, user == owner)) > 0 || appKeepsMemory(spec) ||
		user == owner || spec.MaintainedBy(user)
}

// refreshedCollections are owner's app slug's collections whose blueprint
// has them refresh on their own, as "Name (every week)".
func refreshedCollections(owner, slug string) []string {
	var out []string
	for _, c := range ListCollections(personStore(owner), owner) {
		if c.OwningApp == slug && c.Owner == owner && c.Refresh != "" {
			out = append(out, c.Name+" (every "+c.Refresh+")")
		}
	}
	return out
}

// appCuration is the owner's switch: whether the app's collections refresh
// on their own on this copy of the app.
func (T *CustomApps) appCuration(a *appCall) (any, error) {
	if !a.mine() {
		return nil, jsonapi.Forbidden("the app's owner sets this")
	}
	return map[string]any{"keep_current": !applocal.AppCurationOff(a.owner, a.spec.Slug)}, nil
}

func (T *CustomApps) saveAppCuration(a *appCall, in struct {
	KeepCurrent bool `json:"keep_current"`
}) (any, error) {
	if !a.mine() {
		return nil, jsonapi.Forbidden("the app's owner sets this")
	}
	applocal.SetAppCurationOff(a.owner, a.spec.Slug, !in.KeepCurrent)
	return map[string]any{"ok": true}, nil
}

// curationSection is the owner's "Keep its collections current" switch, for
// an app whose collections refresh on their own; nil otherwise. It is how
// this copy of the app runs, never part of the app: its blueprint keeps how
// often each one refreshes either way.
func curationSection(spec AppSpec, user, owner, base string) *ui.Section {
	if user != owner {
		return nil
	}
	cols := refreshedCollections(owner, spec.Slug)
	if len(cols) == 0 {
		return nil
	}
	return &ui.Section{
		Title:    "Collections",
		Subtitle: "Its collections look for new material on their own: " + strings.Join(cols, ", ") + ".",
		Body: ui.FormPanel{
			Source: base + "/_settings/curation",
			Fields: []ui.FormField{{Field: "keep_current", Label: "Keep its collections current", Type: "toggle",
				Help: "Off: they keep what they have and look for nothing new on their own. How often each one would is part of the app, and stays as it is."}},
		},
	}
}
