package customapps

// How an app lends its owner's credentials to the people using it, the way a
// shared agent does (docs/design/app-credentials.md, steps 2 and 3).
//
// A run for someone other than the owner reaches credentials as that person
// (tools/appscript, core's appLent): their own first, then the owner's as
// the app lends it. This is where the owner says how: one choice per
// credential on the app's Settings page, "What it reaches", from the same
// four an agent's table offers, and a switch that keeps a credential out of
// the app altogether. The choices are this deployment's (core/applocal),
// never part of the app.
//
// The table lists the owner's credentials the app names or uses: those a
// script declares (fetch_via:<name>), those an imported app's names are
// mapped to, and those its runs have gone out through, recorded the first
// time. A credential nobody chose for is lent for reads.

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/applocal"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/ui"
	"github.com/cmcoffee/gohort/tools/appscript"
)

// The four ways to lend a credential, as an agent's table names them.
const (
	lendRead     = "read"
	lendOwn      = "own"
	lendWrite    = "write"
	lendWithhold = "withhold"
)

// withAppCredentials gives a run of owner's app slug its credential rules:
// how it lends each credential (read per call, so a choice made mid-run
// applies to the next call), the ones kept out, and the record of use.
func withAppCredentials(j *appscript.Job, owner, slug string) {
	j.CredentialLend = func(name string) string {
		return applocal.LoadAppCredentials(owner, slug).Choice(name)
	}
	j.CredentialsOff = applocal.LoadAppCredentials(owner, slug).Off
	j.CredentialUsed = func(name string) { applocal.NoteAppCredentialUsed(owner, slug, name) }
}

// appCredentialNames is the owner's credentials the app names or uses, in
// name order: declared by a script, mapped from an imported app's names, or
// gone out through by a run. Only the owner's own credentials: a deployment
// credential is not theirs to lend.
func appCredentialNames(owner string, spec AppSpec, creds applocal.AppCredentials) []string {
	seen := map[string]bool{}
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			return
		}
		if _, ok := Secure().LoadUser(owner, name); ok {
			seen[name] = true
		}
	}
	declare := func(caps []string) {
		for _, c := range caps {
			if n, ok := strings.CutPrefix(strings.TrimSpace(c), "fetch_via:"); ok {
				add(n)
			}
		}
	}
	for _, ds := range spec.DataSources {
		declare(ds.Capabilities)
	}
	for _, a := range spec.Actions {
		declare(a.Capabilities)
	}
	for _, n := range applocal.LoadAppNeeds(owner, spec.Slug) {
		if n.Kind == "credential" && !n.Off {
			add(n.Map)
		}
	}
	for name := range creds.Used {
		add(name)
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// mayManageReach reports whether user sets how the app lends: its owner, or
// one of its maintainers, who may only narrow (maintainerMayLend).
func mayManageReach(a *appCall) bool {
	return a.mine() || a.spec.MaintainedBy(a.User)
}

// maintainerMayLend is the rule an agent's maintainers are held to: Theirs
// and Disallow always, back to reads only from reads or writes, and never
// the owner's key lent for writes.
func maintainerMayLend(current, next string) bool {
	switch next {
	case lendOwn, lendWithhold:
		return true
	case lendRead:
		return current == lendRead || current == lendWrite
	}
	return false
}

// appReachRows is "What it reaches": each credential, where it goes, how the
// app lends it and whether the app uses it at all.
func (T *CustomApps) appReachRows(a *appCall) (any, error) {
	if !mayManageReach(a) {
		return nil, jsonapi.NotFound("no such page")
	}
	creds := applocal.LoadAppCredentials(a.owner, a.spec.Slug)
	rows := []map[string]any{}
	for _, name := range appCredentialNames(a.owner, a.spec, creds) {
		c, _ := Secure().LoadUser(a.owner, name)
		goes := strings.TrimSpace(c.BaseURL)
		if goes == "" {
			goes = strings.TrimSpace(c.AllowedURLPattern)
		}
		how := "named by its scripts"
		if t, ok := creds.Used[name]; ok {
			how = "used since " + t.Format("Jan 2, 2006")
		}
		rows = append(rows, map[string]any{
			"name": name, "goes": goes, "how": how,
			"lend": creds.Choice(name), "used": !creds.IsOff(name), "decide": true,
		})
	}
	return map[string]any{"rows": rows}, nil
}

// saveAppLend is a row's choice: POST _settings/reach {name, lend}.
func (T *CustomApps) saveAppLend(a *appCall, in struct {
	Name string `json:"name"`
	Lend string `json:"lend"`
}) (any, error) {
	if !mayManageReach(a) {
		return nil, jsonapi.NotFound("no such page")
	}
	name, lend := strings.TrimSpace(in.Name), strings.TrimSpace(in.Lend)
	switch lend {
	case lendRead, lendOwn, lendWrite, lendWithhold:
	default:
		return nil, jsonapi.BadRequest("lend is read, own, write or withhold")
	}
	if !T.appCredentialListed(a, name) {
		return nil, jsonapi.BadRequest("this app does not use a credential of yours named " + name)
	}
	current := applocal.LoadAppCredentials(a.owner, a.spec.Slug).Choice(name)
	if !a.mine() && !maintainerMayLend(current, lend) {
		return nil, jsonapi.Forbidden("only the owner can lend their key further: a maintainer may take it back to reads, to each person's own, or withhold it")
	}
	applocal.UpdateAppCredentials(a.owner, a.spec.Slug, func(c *applocal.AppCredentials) {
		if c.Lend == nil {
			c.Lend = map[string]string{}
		}
		c.Lend[name] = lend
	})
	Log("[customapps] %s: %s set how %s lends %q to the people using it: %s", a.spec.Slug, a.User, a.owner, name, lend)
	return map[string]any{"ok": true}, nil
}

// saveAppCredentialUsed is a row's "Used by the app" switch: POST
// _settings/reach/used/{name} {used}. A maintainer may turn one off, never
// back on.
func (T *CustomApps) saveAppCredentialUsed(a *appCall, in struct {
	Used bool `json:"used"`
}) (any, error) {
	if !mayManageReach(a) {
		return nil, jsonapi.NotFound("no such page")
	}
	name := strings.TrimSpace(a.R.PathValue("name"))
	if !T.appCredentialListed(a, name) {
		return nil, jsonapi.BadRequest("this app does not use a credential of yours named " + name)
	}
	if in.Used && !a.mine() {
		return nil, jsonapi.Forbidden("only the owner can let the app use their key again")
	}
	applocal.UpdateAppCredentials(a.owner, a.spec.Slug, func(c *applocal.AppCredentials) {
		off := c.Off[:0]
		for _, n := range c.Off {
			if n != name {
				off = append(off, n)
			}
		}
		if !in.Used {
			off = append(off, name)
		}
		c.Off = off
	})
	return map[string]any{"ok": true}, nil
}

// appCredentialListed reports whether name is one of the credentials the
// app's table lists, the only ones a choice may be made for.
func (T *CustomApps) appCredentialListed(a *appCall, name string) bool {
	for _, n := range appCredentialNames(a.owner, a.spec, applocal.LoadAppCredentials(a.owner, a.spec.Slug)) {
		if n == name {
			return true
		}
	}
	return false
}

// reachSection is the Settings page's "What it reaches", for the app's owner
// and maintainers; nil for anyone else.
func reachSection(spec AppSpec, user, owner, base string) *ui.Section {
	if user != owner && !spec.MaintainedBy(user) {
		return nil
	}
	maintainer := user != owner
	read := ui.SelectOption{Value: lendRead, Label: "Mine for reads", Help: "They read with your key. A write waits for their own account, and once they connect it every call goes out as them."}
	theirs := ui.SelectOption{Value: lendOwn, Label: "Theirs", Help: "They use their own account for it, reads included."}
	write := ui.SelectOption{Value: lendWrite, Label: "Mine, writes too", Help: "Everything goes through your key until they connect their own: what they write arrives as you."}
	withhold := ui.SelectOption{Value: lendWithhold, Label: "Disallow", Help: "Their runs of the app do not reach it. Yours do."}
	choices := []ui.SelectOption{read, theirs, write, withhold}
	if maintainer {
		read.Label, read.Help = "Owner's for reads", "Reads go through the owner's key; from writes, this takes the writes back. Only the owner can lend it further."
		choices = []ui.SelectOption{read, theirs, withhold}
	}
	return &ui.Section{
		Title:    "What it reaches",
		Subtitle: "Your credentials this app uses, and how it lends each to the people you share it with.",
		Detail: "When someone else uses the app, its scripts reach your credentials as THEM: their own account first, then yours as chosen here. " +
			"Mine for reads is the usual one: they read with your key, and a write waits for their own account (Extensions, Connected accounts). " +
			"Theirs makes them use their own for everything; Mine, writes too sends their writes as you; Disallow keeps it from their runs. " +
			"Your own runs, and the app's scheduled ones, use your keys as always. Turn Used by the app off to keep a credential out of it entirely, yours included.\n\n" +
			"Listed: the credentials its scripts name, the ones an imported app's names are mapped to, and any it has used.",
		Body: ui.Table{
			Source: base + "/_settings/reach",
			RowKey: "name",
			Columns: []ui.Col{
				{Field: "name", Label: "Credential", Flex: 1},
				{Field: "goes", Label: "Goes to", Flex: 2},
				{Field: "how", Label: "", Mute: true, Flex: 1},
			},
			EmptyText: "The app reaches no credential of yours.",
			RowActions: []ui.RowAction{
				{Type: "segmented", Field: "lend", OnlyIf: "decide", PostTo: base + "/_settings/reach", Options: choices},
				{Type: "toggle", Field: "used", Label: "Used by the app", PostTo: base + "/_settings/reach/used/{name}"},
			},
		},
	}
}

// appLends is what other people's apps lend person, for core
// (SecureAPI.SetAppLends): each owner and credential lent in at least one
// app shared with them that does not withhold it, with the apps' names.
func (T *CustomApps) appLends(person string) [][3]string {
	person = strings.TrimSpace(person)
	if person == "" {
		return nil
	}
	type key struct{ owner, name string }
	names := map[key][]string{}
	for slug, owner := range ListSharedOwners(T.DB, sharedAppsIndex) {
		if owner == person {
			continue
		}
		spec, ok := loadSpec(owner, slug)
		if !ok || spec.Disabled || !appReaches(spec, person) {
			continue
		}
		creds := applocal.LoadAppCredentials(owner, slug)
		for _, name := range appCredentialNames(owner, spec, creds) {
			if creds.IsOff(name) || creds.Choice(name) == lendWithhold {
				continue
			}
			app := strings.TrimSpace(spec.Name)
			if app == "" {
				app = slug
			}
			k := key{owner, name}
			names[k] = append(names[k], app)
		}
	}
	out := make([][3]string, 0, len(names))
	for k, apps := range names {
		sort.Strings(apps)
		out = append(out, [3]string{k.owner, k.name, strings.Join(apps, ", ")})
	}
	sort.Slice(out, func(i, j int) bool { return out[i][0]+out[i][1] < out[j][0]+out[j][1] })
	return out
}

// appReachRoutes serves "What it reaches".
func (T *CustomApps) appReachRoutes() {
	T.HandleFunc("/{slug}/_settings/reach", jsonapi.Methods{
		http.MethodGet:  T.appGet(T.appReachRows),
		http.MethodPost: appPost(T, T.saveAppLend),
	}.Serve)
	T.HandleFunc("/{slug}/_settings/reach/used/{name}", appPost(T, T.saveAppCredentialUsed))
	T.HandleFunc("/{slug}/needs-account", jsonapi.Methods{http.MethodGet: T.appGet(T.appNeedsAccount)}.Serve)
}

// needsAccount notes, for a person using someone else's app, the credentials
// a data source of it waited on their own account for, so the app's page can
// say so: a data source's refusal reaches only its script, which may print an
// empty list and say nothing. In memory, kept needsAccountKept, and dropped
// for a credential once they have connected their own.
var needsAccount = struct {
	sync.Mutex
	m map[string]map[string]time.Time // owner/slug/person -> credential -> when
}{m: map[string]map[string]time.Time{}}

// needsAccountKept is how long a note stays to be shown.
const needsAccountKept = 15 * time.Minute

// noteNeedsAccount records that a run of owner's app slug for person waited
// on their own account for credential name.
func noteNeedsAccount(owner, slug, person, name string) {
	key := owner + "/" + slug + "/" + person
	needsAccount.Lock()
	defer needsAccount.Unlock()
	if needsAccount.m[key] == nil {
		needsAccount.m[key] = map[string]time.Time{}
	}
	needsAccount.m[key][name] = time.Now()
}

// needsAccountFor is the credentials person still needs their own account
// for in owner's app slug, by name: noted lately, and not connected since.
func needsAccountFor(owner, slug, person string) []string {
	key := owner + "/" + slug + "/" + person
	needsAccount.Lock()
	var names []string
	for name, at := range needsAccount.m[key] {
		if time.Since(at) > needsAccountKept {
			delete(needsAccount.m[key], name)
			continue
		}
		names = append(names, name)
	}
	needsAccount.Unlock()
	out := names[:0]
	for _, name := range names {
		if !Secure().HasSharedAccount(owner, name, person) {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// appNeedsAccount answers needs-account: the credentials the caller's runs
// of this app waited on their own account for. Nothing for its owner, whose
// runs use their own keys.
func (T *CustomApps) appNeedsAccount(a *appCall) (any, error) {
	names := []string{}
	if !a.mine() {
		names = append(names, needsAccountFor(a.owner, a.spec.Slug, a.User)...)
	}
	return map[string]any{"names": names}, nil
}

// accountNeedsJS is served on an app's page for someone other than its
// owner. A little after the page loads, and again while it is open, it asks
// whether the app's data waited on their own account for something, and if
// so says which and where to connect it, once, below the header. Drawn by
// gohort, so it shows whatever the app's page does with the empty answer.
func accountNeedsJS(base string) string {
	b, _ := json.Marshal(base)
	return `<script>(function(){var base=` + string(b) + `,shown=false,waits=[1500,5000,15000,40000,90000],i=0;` +
		`function show(names){if(shown||!names||!names.length)return;shown=true;` +
		`var bar=document.createElement("div");bar.setAttribute("role","status");bar.setAttribute("data-needs-account","");` +
		`bar.style.cssText="display:flex;gap:12px;align-items:center;flex-wrap:wrap;margin:8px 0;padding:8px 12px;border:1px solid var(--border);border-radius:6px;background:var(--bg-1)";` +
		`var t=document.createElement("span");t.style.flex="1 1 240px";t.textContent="Part of this app needs your own "+names.join(" and ")+" account, so some of what it shows may be missing. Connect yours under Extensions, Connected accounts, then reload the page.";` +
		`var a=document.createElement("a");a.className="ui-row-btn";a.href="/extensions/";a.textContent="Connected accounts";bar.appendChild(t);bar.appendChild(a);` +
		`var h=document.querySelector(".ui-page-header");if(h&&h.parentNode){h.parentNode.insertBefore(bar,h.nextSibling);}else{var r=document.getElementById("ui-root")||document.body;r.insertBefore(bar,r.firstChild);}}` +
		`function ask(){fetch(base+"needs-account",{credentials:"same-origin"}).then(function(r){return r.json();}).then(function(d){show(d&&d.names);}).catch(function(){}).then(function(){if(!shown&&i<waits.length)setTimeout(ask,waits[i++]);});}` +
		`setTimeout(ask,waits[i++]);})();</script>`
}
