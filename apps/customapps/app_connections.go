package customapps

// An imported app's connections (docs/design/app-history.md, piece F2): what
// it names that only a deployment has (a credential, a tool it calls, a voice,
// a file store), each mapped to one of its owner's here or gone without. Its
// owner answers them in the dialog an import opens when something did not
// match, and changes them later under the app's ⚙, Connections: the same
// form in both. The app stays switched off until every one is answered.

import (
	"context"
	"fmt"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/applocal"
	"github.com/cmcoffee/gohort/core/appstores"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/media"
	"github.com/cmcoffee/gohort/core/ui"
)

// needOff is the select value of "don't map": the app goes without it.
const needOff = "__off__"

// connectionsRoutes serves the form's record and the form itself.
func (T *CustomApps) connectionsRoutes() {
	// The owner's own app, switched off or not: an imported app is
	// answered here before it may run, so these do not go through inApp,
	// which refuses a switched-off app.
	T.HandleFunc("/{slug}/_settings/connections", jsonapi.Methods{
		"GET": jsonapi.Get(T.DB, func(c *jsonapi.Call) (any, error) {
			a, err := T.ownApp(c)
			if err != nil {
				return nil, err
			}
			return T.appConnections(a)
		}),
		"POST": jsonapi.Post(T.DB, func(c *jsonapi.Call, in map[string]any) (any, error) {
			a, err := T.ownApp(c)
			if err != nil {
				return nil, err
			}
			return T.saveAppConnections(a, in)
		}),
	}.Serve)
	T.HandleFunc("/{slug}/_settings/connections/form", jsonapi.Methods{"GET": jsonapi.Get(T.DB, func(c *jsonapi.Call) (any, error) {
		a, err := T.ownApp(c)
		if err != nil {
			return nil, err
		}
		return T.appConnectionsForm(a)
	})}.Serve)
}

// ownApp is the caller's own app the route names, switched off or not.
func (T *CustomApps) ownApp(c *jsonapi.Call) (*appCall, error) {
	spec, ok := loadSpec(c.User, c.R.PathValue("slug"))
	if !ok {
		return nil, errNoApp
	}
	return &appCall{Call: c, spec: spec, owner: c.User}, nil
}

// needField is the form field of the i'th need.
func needField(i int) string { return fmt.Sprintf("n%d", i) }

// needLabel says what a need is, as a person reads it.
func needLabel(n applocal.AppNeed) string {
	kind := map[string]string{"credential": "Credential", "tool": "Tool", "voice": "Voice", "store": "File store"}[n.Kind]
	return kind + " " + n.Name
}

// needOptions are what a need of kind may be mapped to here: the owner's
// own, by name, then going without.
func needOptions(ctx context.Context, owner, slug string, n applocal.AppNeed) []ui.SelectOption {
	seen := map[string]bool{}
	var names []string
	add := func(name string) {
		if name = strings.TrimSpace(name); name != "" && !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	switch n.Kind {
	case "credential":
		for _, c := range Secure().ListUser(owner) {
			add(c.Name)
		}
		for _, c := range Secure().List() {
			add(c.Name)
		}
	case "tool":
		for _, p := range LoadPersistentTempTools(RootDB, owner) {
			add(p.Tool.Name)
		}
		for _, l := range AdoptedToolsFor(RootDB, owner) {
			add(l.Tool.Name)
		}
		for _, t := range RegisteredChatTools() {
			add(t.Name())
		}
	case "voice":
		for _, v := range media.SpeechVoices(ctx, media.GetSpeechConfig()) {
			add(v.ID)
		}
	case "store":
		if cat, ok := appstores.Provider.(appstores.Catalog); ok {
			for _, s := range cat.Stores(owner, slug) {
				add(s.Store)
			}
		}
	}
	sort.Strings(names)
	out := []ui.SelectOption{{Value: "", Label: "Not answered yet"}}
	for _, name := range names {
		out = append(out, ui.SelectOption{Value: name, Label: name})
	}
	return append(out, ui.SelectOption{Value: needOff, Label: "Don't map: it goes without"})
}

// needHelp says what answering n does, and how to make a new one.
func needHelp(n applocal.AppNeed) string {
	switch n.Kind {
	case "credential":
		return "Pick one of your credentials, or add a new one under Account, API credentials, and pick it here. Without it, what reaches it says so."
	case "tool":
		return "Pick one of your tools, or have Builder make one and pick it here. Without it, what calls it says so."
	case "voice":
		return "Pick a voice offered here. Without it, the agent speaks in the deployment's voice."
	case "store":
		what := "read"
		if n.Write {
			what += ", write"
		}
		if n.Commands {
			what += ", run its commands"
		}
		if n.Pull {
			what += ", pull into it"
		}
		return "Pick one of the stores you reach: the app asks an administrator for it (" + what + "), as an app asks for a store. Without it, what uses it says so."
	}
	return ""
}

// connectionsForm is the form answering owner's app slug's needs, and how
// many are unanswered.
func connectionsForm(ctx context.Context, base, owner, slug string) (ui.FormPanel, int) {
	needs := applocal.LoadAppNeeds(owner, slug)
	var fields []ui.FormField
	unanswered := 0
	for i, n := range needs {
		if !n.Answered() {
			unanswered++
		}
		fields = append(fields, ui.FormField{Field: needField(i), Label: needLabel(n), Type: "select",
			Options: needOptions(ctx, owner, slug, n), Help: needHelp(n)})
	}
	return ui.FormPanel{Source: base + "/_settings/connections", Fields: fields}, unanswered
}

// appConnections is the form's record: each need's answer.
func (T *CustomApps) appConnections(a *appCall) (any, error) {
	if !a.mine() {
		return nil, jsonapi.Forbidden("the app's owner answers what it needs")
	}
	out := map[string]any{}
	for i, n := range applocal.LoadAppNeeds(a.owner, a.spec.Slug) {
		switch {
		case n.Off:
			out[needField(i)] = needOff
		default:
			out[needField(i)] = n.Map
		}
	}
	return out, nil
}

// saveAppConnections records the answers: a store newly mapped asks an
// administrator for it, as an app asks for a store.
func (T *CustomApps) saveAppConnections(a *appCall, in map[string]any) (any, error) {
	if !a.mine() {
		return nil, jsonapi.Forbidden("the app's owner answers what it needs")
	}
	needs := applocal.LoadAppNeeds(a.owner, a.spec.Slug)
	for i := range needs {
		raw, ok := in[needField(i)]
		if !ok {
			continue
		}
		v := strings.TrimSpace(fmt.Sprint(raw))
		was := needs[i].Map
		needs[i].Map, needs[i].Off = "", false
		switch v {
		case needOff:
			needs[i].Off = true
		case "", "<nil>":
		default:
			needs[i].Map = v
		}
		if n := needs[i]; n.Kind == "store" && n.Map != "" && n.Map != was {
			name := appstores.GrantRequestName(n.Map, a.spec.Slug, n.Write, n.Commands, n.Pull)
			why := fmt.Sprintf("The imported app %q uses a store it calls %q; its owner mapped it to this one.", a.spec.Name, n.Name)
			if err := CreatePromotionRequest(AuthDB(), a.owner, appstores.GrantRequestKind, name, why); err != nil {
				Log("[customapps] %s/%s: the store request for %q could not be filed: %v", a.owner, a.spec.Slug, n.Map, err)
			}
		}
	}
	applocal.SaveAppNeeds(a.owner, a.spec.Slug, needs)
	return map[string]any{"ok": true}, nil
}

// appConnectionsForm is the form as a component, for the dialog an import
// opens, with how many needs wait for an answer.
func (T *CustomApps) appConnectionsForm(a *appCall) (any, error) {
	if !a.mine() {
		return nil, jsonapi.Forbidden("the app's owner answers what it needs")
	}
	form, unanswered := connectionsForm(a.R.Context(), T.WebPath()+"/"+a.spec.Slug, a.owner, a.spec.Slug)
	return map[string]any{"name": a.spec.Name, "unanswered": unanswered, "form": form}, nil
}

// connectionsSection is the Settings page's Connections, for the owner of
// an app that names what only a deployment has; nil otherwise.
func connectionsSection(ctx context.Context, spec AppSpec, user, owner, base string) *ui.Section {
	if user != owner || len(applocal.LoadAppNeeds(owner, spec.Slug)) == 0 {
		return nil
	}
	form, unanswered := connectionsForm(ctx, base, owner, spec.Slug)
	sub := "What this app names that only a deployment has, and what it means here. Change any of them; one it goes without, map later."
	if unanswered > 0 {
		sub = fmt.Sprintf("%d of what this app names that only a deployment has %s no answer yet: map each to one of yours, or let it go without. The app stays switched off until every one is answered.",
			unanswered, map[bool]string{true: "has", false: "have"}[unanswered == 1])
	}
	return &ui.Section{Title: "Connections", Subtitle: sub, Body: form}
}

// needsRefusal is why owner's app slug may not be switched on yet: needs
// with no answer. "" when every one is answered.
func needsRefusal(owner, slug string) string {
	n := 0
	for _, need := range applocal.LoadAppNeeds(owner, slug) {
		if !need.Answered() {
			n++
		}
	}
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("this app needs %d answer%s first: under its ⚙, Connections, map each to one of yours or let it go without", n, map[bool]string{true: "", false: "s"}[n == 1])
}
