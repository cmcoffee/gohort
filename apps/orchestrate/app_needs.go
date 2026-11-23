package orchestrate

// What an app names that only a deployment has (docs/design/app-history.md,
// piece F2): the credentials its scripts and tools reach, the tools it calls
// that are neither gohort's nor its own, its agents' voices, and the file
// stores it was granted. An imported app lists each; one found here is
// mapped to itself, and one that is not waits for its owner to map it to one
// of theirs or go without (core.AppNeed, kept on this deployment and never
// exported). What the app's names mean here is then read wherever the app
// reaches one (core.AppNeedAlias): its scripts' and agents' credentials and
// tools, its agents' voices, its scripts' stores.

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/applocal"
	"github.com/cmcoffee/gohort/core/appstores"
	"github.com/cmcoffee/gohort/core/apptools"
	"github.com/cmcoffee/gohort/core/media"
)

// appStoresFile is the export-only file of an app's recipe that names the
// file stores it was granted, and what it may do with each.
const appStoresFile = "stores.json"

// appStoreNeeds is what owner's app slug was granted of the file stores, as
// needs, for its export.
func appStoreNeeds(owner, slug string) []applocal.AppNeed {
	if appstores.Provider == nil {
		return nil
	}
	var out []applocal.AppNeed
	for _, g := range appstores.Provider.GrantsFor(appstores.App{Owner: owner, Slug: slug}) {
		out = append(out, applocal.AppNeed{Kind: "store", Name: g.Store, Write: g.Write, Commands: g.Commands, Pull: g.Pull})
	}
	return out
}

// mapAppAgentTools makes the turn's agent's tool list, when the agent is an
// app's, what the app's names for them mean here: a mapped one under its
// mapping, one the app goes without left out. In the turn's copy only, once.
func (t *chatTurn) mapAppAgentTools() {
	app := strings.TrimSpace(t.agent.OwningApp)
	if t.appToolsMapped || app == "" || len(t.agent.AllowedTools) == 0 || isNoToolsSentinel(t.agent.AllowedTools) {
		return
	}
	t.appToolsMapped = true
	owner := firstNonEmptyStr(t.agent.Owner, t.user)
	if len(applocal.LoadAppNeeds(owner, app)) == 0 {
		return
	}
	out := make([]string, 0, len(t.agent.AllowedTools))
	changed := false
	for _, n := range t.agent.AllowedTools {
		to, off := applocal.AppNeedAlias(owner, app, "tool", n)
		if off || to != n {
			changed = true
		}
		if !off {
			out = append(out, to)
		}
	}
	if !changed {
		return
	}
	if len(out) == 0 {
		// Every tool it names gone without: no tools, never all of them.
		out = []string{noToolsSentinel}
	}
	t.agent.AllowedTools = out
}

// appNeedsOf is what owner's app slug names that only a deployment has, each
// with whether it is found here: stores are those its recipe said it was
// granted (stores).
func appNeedsOf(owner, slug string, stores []applocal.AppNeed) (needs []applocal.AppNeed, found map[string]bool) {
	spec, ok := LoadAppSpec(owner, slug)
	if !ok {
		return nil, nil
	}
	o, ok := agenthostApp()
	if !ok || o.DB == nil {
		return nil, nil
	}
	udb := UserDB(o.DB, owner)
	found = map[string]bool{}
	seen := map[string]bool{}
	add := func(kind, name string, have bool, store applocal.AppNeed) {
		name = strings.TrimSpace(name)
		key := kind + "\x00" + name
		if name == "" || seen[key] {
			return
		}
		seen[key] = true
		n := store
		n.Kind, n.Name = kind, name
		needs = append(needs, n)
		found[key] = have
	}
	credHere := func(name string) bool {
		_, ok := Secure().Resolve(name, owner)
		return ok
	}
	toolHere := func(name string) bool {
		name = canonicalToolName(name)
		if _, ok := FindChatTool(name); ok || ToolNameClaimed(name) {
			return true
		}
		if _, ok := apptools.ByName(owner, slug, name); ok {
			return true
		}
		if _, ok := UserToolByName(udb, owner, name); ok {
			return true
		}
		for _, l := range AdoptedToolsFor(udb, owner) {
			if l.Tool.Name == name {
				return true
			}
		}
		for _, n := range append(availableWorkerToolNames(), frameworkAlwaysOnToolNames()...) {
			if n == name {
				return true
			}
		}
		return false
	}
	caps := func(list []string) {
		for _, c := range list {
			c = strings.TrimSpace(c)
			switch {
			case strings.HasPrefix(c, "fetch_via:"), strings.HasPrefix(c, "secret:"):
				name := c[strings.IndexByte(c, ':')+1:]
				add("credential", name, credHere(name), applocal.AppNeed{})
			case strings.HasPrefix(c, "tool:"):
				name := strings.TrimPrefix(c, "tool:")
				add("tool", name, toolHere(name), applocal.AppNeed{})
			}
		}
	}
	for _, ds := range spec.DataSources {
		caps(ds.Capabilities)
	}
	for _, a := range spec.Actions {
		caps(a.Capabilities)
	}
	for _, at := range apptools.Load(owner, slug) {
		if c := strings.TrimSpace(at.Tool.Credential); c != "" && c != "no_auth" {
			add("credential", c, credHere(c), applocal.AppNeed{})
		}
		caps(at.Tool.HookCapabilities)
	}
	var voices map[string]bool
	for _, a := range listAgents(udb, owner) {
		if a.OwningApp != slug || a.Owner != owner {
			continue
		}
		if !isNoToolsSentinel(a.AllowedTools) {
			for _, n := range a.AllowedTools {
				if n = strings.TrimSpace(n); n != "" && n != "*" {
					add("tool", n, toolHere(n), applocal.AppNeed{})
				}
			}
		}
		if v := strings.TrimSpace(a.Voice); v != "" {
			if voices == nil {
				voices = map[string]bool{}
				for _, x := range media.SpeechVoices(context.Background(), media.GetSpeechConfig()) {
					voices[x.ID] = true
				}
			}
			add("voice", v, voices[v], applocal.AppNeed{})
		}
	}
	granted := map[string]bool{}
	for _, g := range appStoreNeeds(owner, slug) {
		granted[g.Name] = true
	}
	for _, s := range stores {
		add("store", s.Name, granted[s.Name], s)
	}
	sort.SliceStable(needs, func(i, j int) bool {
		if needs[i].Kind != needs[j].Kind {
			return needs[i].Kind < needs[j].Kind
		}
		return needs[i].Name < needs[j].Name
	})
	return needs, found
}

// recordAppNeeds lists owner's app slug's needs on this deployment, after
// its import: each found here mapped to itself, each not left for its owner
// to answer. An answer already given is kept.
func recordAppNeeds(owner, slug string, stores []applocal.AppNeed) []applocal.AppNeed {
	needs, found := appNeedsOf(owner, slug, stores)
	have := map[string]applocal.AppNeed{}
	for _, n := range applocal.LoadAppNeeds(owner, slug) {
		have[n.Kind+"\x00"+n.Name] = n
	}
	for i, n := range needs {
		key := n.Kind + "\x00" + n.Name
		if prev, ok := have[key]; ok && prev.Answered() {
			needs[i].Map, needs[i].Off = prev.Map, prev.Off
			continue
		}
		if found[key] {
			needs[i].Map = n.Name
		}
	}
	applocal.SaveAppNeeds(owner, slug, needs)
	return needs
}

// unansweredNeeds are those of needs not answered yet.
func unansweredNeeds(needs []applocal.AppNeed) []applocal.AppNeed {
	var out []applocal.AppNeed
	for _, n := range needs {
		if !n.Answered() {
			out = append(out, n)
		}
	}
	return out
}

// readAppStoresFile reads the stores an app's recipe said it was granted.
func readAppStoresFile(files map[string][]byte) []applocal.AppNeed {
	var out []applocal.AppNeed
	if b, ok := files[appStoresFile]; ok {
		json.Unmarshal(b, &out)
	}
	return out
}
