package core

import (
	"net/http"
	"net/url"
	"strings"
	"sync"
)

// LiveEntry is a JSON-serializable summary of an active or queued session.
type LiveEntry struct {
	ID      string `json:"id"`
	Label   string `json:"topic"` // "topic" for backwards compat with JS
	Queued  bool   `json:"queued,omitempty"`
	Spawned bool   `json:"spawned,omitempty"` // spawned by a parent app
	// Background marks work that started WITHOUT the viewer: a scheduled fire,
	// an inbound message, a standing agent waking on its own. The live pill
	// paints it differently, because "your turn is running" needs no
	// announcement and "something acted while you were reading" does.
	//
	// Providers set it; nothing here infers it. An app knows whether it was
	// asked, and the framework does not.
	Background bool   `json:"background,omitempty"`
	Status     string `json:"status,omitempty"` // last status message
	App        string `json:"app,omitempty"`    // which app owns this session
	Path       string `json:"path,omitempty"`   // web path prefix
	URL        string `json:"url,omitempty"`    // full reconnect URL (if set, used instead of path+id)
	Order      int    `json:"order,omitempty"`  // display order for the live ribbon (lower = earlier); ties break by App name
	// Href is the resolved destination for "take me back to this work",
	// filled in by /api/live rather than by providers: it collapses URL
	// and Path+ID to one link AND applies the viewer's app access, so
	// every surface (live pill, Monitor table) agrees on where a row goes
	// without each re-deriving it. Empty means there is nowhere to send
	// this viewer — either the work has no owning page or access says no.
	Href string `json:"href,omitempty"`
	// OwnerURL is where the work's OWNER goes to rejoin it — the conversation
	// itself — when that is a different place from where everyone else may
	// look. A running chat turn is a thread its owner was in; to anyone else
	// it is a row on the monitor. Never serialized: /api/live substitutes it
	// for URL when the viewer is the owner, so the access check and Href
	// resolution below see one destination, the right one for this viewer.
	// Empty means the owner goes where everyone goes.
	OwnerURL string `json:"-"`
	// CancelURL is where a POST stops this work, when the owning app offers a
	// way to stop it. Empty means it cannot be stopped from here, which is the
	// honest answer for most entries — a turn the viewer is watching ends on
	// its own, and a queued item is removed by its own queue control.
	//
	// It exists for work that OUTLIVES the turn that started it. A background
	// render or a dispatched sub-agent can run for minutes with nothing on
	// screen to stop it: the run registry has had a working cancel endpoint all
	// along and nothing ever called it, so "wait it out" was the only option.
	//
	// The APP resolves the path — core/ui only learns that an entry declares
	// one, and posts to it.
	CancelURL string `json:"cancel_url,omitempty"`
	// Owner is the user whose work this is. Never serialized — it exists so
	// /api/live can decide whether THIS viewer may see the entry's Label,
	// which for most providers is user content (the chat message, the
	// research question, the debate topic). The live ribbon is global and
	// untenanted by design, so without this every user reads every other
	// user's prompts off the pill.
	//
	// Empty means "unknown owner", which masks for everyone — fail closed.
	// A provider that hasn't been taught to fill this shows a generic label
	// rather than leaking; that's the safe direction to be wrong in.
	Owner string `json:"-"`
	// PublicLabel says this entry's label contains NO user-authored text, so it
	// may be shown to everyone unmasked.
	//
	// Masking exists because a label is usually something a person typed — a
	// prompt, a research question, a debate topic — and the ribbon is global.
	// Some rows are not that. Work borrowed by a peer is described entirely by
	// the framework ("Peer studio-mac — running a model"), and masking it to
	// "another user" removes the only fact it exists to convey: WHOSE machine
	// is competing for the GPU while your turn waits.
	//
	// Opt-in, and it stays that way. A provider must state that its label is
	// framework-generated; the default remains mask-everything, because the
	// cost of being wrong here is reading someone else's prompt off a pill.
	PublicLabel bool `json:"-"`
}

// MaskedLabel returns the entry's label as viewer should see it: unchanged
// for the owner, generic for anyone else (admins included — an operator who
// needs run detail has the runs registry and pprof, and "logged in as admin"
// shouldn't mean "reads everyone's prompts").
//
// App and Status are deliberately NOT masked. App is the owning app or agent
// name, already shown as its own column, and Status is kind/round/tool —
// operationally useful and not something the user typed.
//
// Any tree-indent prefix survives masking, or the nested view collapses.
func (e LiveEntry) MaskedLabel(viewer string) string {
	if e.Owner != "" && e.Owner == viewer {
		return e.Label
	}
	// A label the framework wrote has nothing to protect. See PublicLabel.
	if e.PublicLabel {
		return e.Label
	}
	indent, _ := splitLiveIndent(e.Label)
	who := e.Owner
	if who == "" {
		who = "another user"
	}
	if e.App != "" {
		return indent + e.App + " · " + who
	}
	return indent + "Active session · " + who
}

// splitLiveIndent peels a live label's leading tree indent (spaces, and the
// "↳ " marker a nested run carries) from its content.
func splitLiveIndent(label string) (indent, rest string) {
	i := 0
	for i < len(label) && label[i] == ' ' {
		i++
	}
	if strings.HasPrefix(label[i:], "↳ ") {
		i += len("↳ ")
	}
	return label[:i], label[i:]
}

// ResolveHref fills Href from the entry's URL, or its Path plus the
// framework's ?reconnect=<id> convention, which the runtime honours on any
// page configured with events_url. Returns "" when the entry offers neither,
// which is the normal case for work with no owning page: agent runs from the
// runs registry and queued tasks carry a label but nothing to return to.
//
// Callers gate on access BEFORE calling this — an entry the viewer can't
// follow should be left with an empty Href, not a link that 403s.
func (e LiveEntry) ResolveHref() string {
	if e.URL != "" {
		return e.URL
	}
	if e.Path == "" || e.ID == "" {
		return ""
	}
	return strings.TrimSuffix(e.Path, "/") + "/?reconnect=" + url.QueryEscape(e.ID)
}

// applyOwnerDestination swaps in the owner's destination when the viewer IS
// the owner. Decided before the app-access check, so an owner who cannot
// reach the app gets no link at all rather than a link to the monitor. An
// entry with no owner has no owner's destination: fail closed, as MaskedLabel
// does.
func (e *LiveEntry) applyOwnerDestination(viewer string) {
	if e.OwnerURL == "" || e.Owner == "" || viewer == "" || e.Owner != viewer {
		return
	}
	e.URL = e.OwnerURL
}

// liveEntryAppPath returns the app mount prefix a live entry points back
// at, or "" when the entry offers no route into an app at all.
//
// "" is the normal case for work that isn't owned by a web surface —
// agent runs from the runs registry and queued tasks carry a label and a
// status but no page to return to. Those are what the Monitor exists for.
func liveEntryAppPath(e LiveEntry) string {
	if e.Path != "" {
		return e.Path
	}
	if !strings.HasPrefix(e.URL, "/") {
		return "" // empty, or an absolute/foreign URL we can't gate
	}
	p := e.URL
	if i := strings.IndexAny(p, "?#"); i >= 0 {
		p = p[:i]
	}
	// First path segment is the mount prefix ("/servitor/?reconnect=x").
	if i := strings.Index(p[1:], "/"); i >= 0 {
		p = p[:i+1]
	}
	return strings.TrimSuffix(p, "/")
}

// userCanReachApp reports whether this viewer may open the app mounted at
// prefix, applying the same two gates the dashboard uses to decide which
// cards to draw: the per-user app grant and the app's own WebRestricted.
//
// Apps registered WebHidden are absent from the dashboard list, so they
// clear on the grant check alone. Hidden means "no card", not "no entry".
func userCanReachApp(r *http.Request, apps []dashApp, prefix string) bool {
	if !UserHasAppAccess(r, prefix) {
		return false
	}
	for _, a := range apps {
		if a.path != prefix || a.app == nil {
			continue
		}
		if ra, ok := a.app.(WebAppRestricted); ok && ra.WebRestricted(r) {
			return false
		}
	}
	return true
}

// LiveProvider returns active sessions for a specific app.
type LiveProvider func() []LiveEntry

var (
	liveProviderMu sync.Mutex
	liveProviders  []LiveProvider
)

// RegisterLiveProvider adds a provider that contributes to the global live view.
func RegisterLiveProvider(p LiveProvider) {
	liveProviderMu.Lock()
	defer liveProviderMu.Unlock()
	liveProviders = append(liveProviders, p)
}

// AllLiveSessions aggregates active sessions from all registered providers plus the global queue.
func AllLiveSessions() []LiveEntry {
	liveProviderMu.Lock()
	providers := make([]LiveProvider, len(liveProviders))
	copy(providers, liveProviders)
	liveProviderMu.Unlock()

	var all []LiveEntry
	for _, p := range providers {
		all = append(all, p()...)
	}
	all = append(all, GlobalQueue().QueuedEntries()...)
	return all
}
