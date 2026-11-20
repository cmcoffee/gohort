package core

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cmcoffee/gohort/core/changes"
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
	// AdminOnly keeps the entry from everyone but administrators. For work
	// with no owner to show it to: an agent loop whose caller named nobody
	// (see ListLoopAs) is still running on this machine, and someone who
	// runs the machine should be able to see it and stop it.
	AdminOnly bool `json:"-"`
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
		return indent + e.App + " - " + who
	}
	return indent + "Active session - " + who
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
	// An app switched off deployment-wide offers no way back to anyone, so a
	// live row that points into one loses its link and falls back to the
	// Monitor — the same treatment as a row the viewer isn't granted.
	if !AppEnabledHere(prefix) {
		return false
	}
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

// liveTopic is the changes topic for the live view: the work running or
// queued now. One for everyone, because /api/live shows every viewer every
// entry (labels masked to their owner), so it is the set that changed, not
// anyone's share of it.
const liveTopic = "live"

// liveWatch turns "the live set may have moved" into a version a page waits
// on. Providers are pull functions, so the set is compared rather than
// trusted: a kick (LiveChanged, or a LiveSessionMap or the queue moving)
// re-reads it shortly after, and a slow reconcile catches the providers that
// cannot say. Either way the topic moves only when the set did: the ids, and
// which are queued or in the background.
var liveWatch struct {
	sync.Mutex
	last    string
	pending bool
	once    sync.Once
}

// LiveChanged says that work an app shows in the live view started, ended or
// moved between queued and running, so every page showing the view hears it
// at once instead of on its next poll.
func LiveChanged() {
	liveWatch.Lock()
	defer liveWatch.Unlock()
	if liveWatch.pending {
		return
	}
	liveWatch.pending = true
	time.AfterFunc(150*time.Millisecond, reconcileLive)
}

// reconcileLive compares the live set with the last one seen.
func reconcileLive() {
	liveWatch.Lock()
	liveWatch.pending = false
	liveWatch.Unlock()
	fp := liveFingerprint(AllLiveSessions())
	liveWatch.Lock()
	moved := fp != liveWatch.last
	liveWatch.last = fp
	liveWatch.Unlock()
	if moved {
		changes.Note(liveTopic)
	}
}

// liveReconcileEvery is how often the live set is compared without a kick,
// for the providers that cannot say they moved.
var liveReconcileEvery = 2 * time.Second

// watchLive starts the slow reconcile, once, when the first page asks.
func watchLive() {
	liveWatch.once.Do(func() {
		reconcileLive()
		go func() {
			for range time.Tick(liveReconcileEvery) {
				reconcileLive()
			}
		}()
	})
}

// liveFingerprint is the live set as reconcileLive compares it.
func liveFingerprint(entries []LiveEntry) string {
	keys := make([]string, 0, len(entries))
	for _, e := range entries {
		keys = append(keys, e.ID+"|"+strconv.FormatBool(e.Queued)+"|"+strconv.FormatBool(e.Background))
	}
	sort.Strings(keys)
	return strings.Join(keys, "\n")
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

// --- Agent loops among the running work ---
//
// Every agent loop shows among the running work (the Monitor, the live pill),
// whatever started it. Most already do: orchestrate lists its runs, a
// pipeline run and a Servitor session list themselves, and each marks the
// context it runs work under (ListedWork) so the loop adds nothing. A loop
// under no such mark lists itself here while it runs: for the owner and in
// the words its caller gave (ListLoopAs), with its round and tools as the
// status, and a Stop. A loop nobody named is listed for administrators only,
// as its app's agent work, and the app is logged once so its caller can be
// named. Loops a listed loop starts run under its mark and add no row.

type loopListKeyT struct{}

var loopListKey loopListKeyT

// loopListing is what a context says about the agent loops run under it:
// already shown somewhere (listed), or whose they are and what to call them.
type loopListing struct {
	listed       bool
	owner, label string
}

// ListedWork marks ctx as work that already has a row among the running work,
// so an agent loop run under it does not add a second one. Whatever lists
// work and runs agent loops under it calls this on the context it hands on.
func ListedWork(ctx context.Context) context.Context {
	return context.WithValue(ctx, loopListKey, loopListing{listed: true})
}

// ListLoopAs names the agent loops run under ctx for the running work: owner
// is the user whose work it is, who sees the label and may stop it; label is
// what it is, in words the app wrote. Never the prompt or a message: everyone
// else sees the row masked, but the owner's own screen is not the only place
// a label ends up. An empty label lists it as the app's agent work.
func ListLoopAs(ctx context.Context, owner, label string) context.Context {
	return context.WithValue(ctx, loopListKey, loopListing{owner: strings.TrimSpace(owner), label: strings.TrimSpace(label)})
}

// loopStopPath is where a POST stops a listed loop (?id=).
const loopStopPath = "/api/live/stop"

var (
	loopRows    = NewLiveSessionMap[struct{}](0)
	loopRowMu   sync.Mutex
	loopRowApp  = map[string]string{} // row id -> the app running the loop
	loopUnnamed sync.Map              // apps already logged for an unnamed loop
)

func init() { RegisterLiveProvider(loopLiveEntries) }

// loopLiveEntries is the loops listed here, as live entries.
func loopLiveEntries() []LiveEntry {
	entries := loopRows.ActiveSessions()
	loopRowMu.Lock()
	defer loopRowMu.Unlock()
	for i := range entries {
		entries[i].App = loopRowApp[entries[i].ID]
		entries[i].CancelURL = loopStopPath + "?id=" + url.QueryEscape(entries[i].ID)
		if entries[i].Owner == "" {
			// The label is the framework's own (loopAppLabel), and the
			// row is for administrators only.
			entries[i].PublicLabel, entries[i].AdminOnly = true, true
		}
	}
	return entries
}

// loopAppLabel is how a loop nobody named is listed: as its app's agent work.
func loopAppLabel(app string) string {
	if app == "" {
		return "Agent work"
	}
	return strings.ToUpper(app[:1]) + app[1:] + " agent work"
}

// loopStepStatus is a round of the loop as a row's status: which round, and
// the tools it called.
func loopStepStatus(st StepInfo) string {
	status := fmt.Sprintf("round %d", st.Round)
	var names []string
	seen := map[string]bool{}
	for _, tc := range st.ToolCalls {
		if tc.Name != "" && !seen[tc.Name] {
			seen[tc.Name] = true
			names = append(names, tc.Name)
		}
	}
	if len(names) > 0 {
		status += ", " + strings.Join(names, ", ")
	}
	return status
}

// listLoop lists the agent loop of app about to run under ctx, unless
// something already lists it. The returned ctx is what the loop runs under,
// so Stop reaches it and the loops it starts add no row; end takes the row
// off when the loop returns. cfg's OnStep is wrapped to keep the status.
func listLoop(ctx context.Context, app string, cfg *AgentLoopConfig) (_ context.Context, end func()) {
	l, _ := ctx.Value(loopListKey).(loopListing)
	if l.listed {
		return ctx, func() {}
	}
	label := l.label
	if l.owner == "" || label == "" {
		label = loopAppLabel(app)
	}
	if l.owner == "" {
		if _, logged := loopUnnamed.LoadOrStore(app, true); !logged {
			Log("[agent_loop] an agent loop of %q names no owner, so it is listed for administrators only (core.ListLoopAs)", app)
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	id := UUIDv4()
	loopRowMu.Lock()
	loopRowApp[id] = app
	loopRowMu.Unlock()
	loopRows.Register(id, label, cancel).SetOwner(l.owner)
	prev := cfg.OnStep
	cfg.OnStep = func(st StepInfo) {
		loopRows.UpdateStatus(id, loopStepStatus(st))
		if prev != nil {
			prev(st)
		}
	}
	return ListedWork(ctx), func() {
		loopRows.AppendEvent(id, struct{}{}, true)
		loopRowMu.Lock()
		delete(loopRowApp, id)
		loopRowMu.Unlock()
		// Ended rows are no longer listed; the map lets go of them a
		// minute on, as every live session map does after its own wait.
		loopRows.ScheduleCleanupAfter(id, time.Minute)
		cancel()
	}
}

// handleLoopStop is POST /api/live/stop?id=: stops a listed loop, for its
// owner, or for an administrator when it has none. Anyone else, and an id
// that is not running, gets the same 404.
func handleLoopStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	id := r.URL.Query().Get("id")
	owner, ok := loopRows.OwnerOf(id)
	if !ok || !(loopRows.MayView(AuthCurrentUser(r), id) || owner == "" && RequestIsAdmin(r)) || !loopRows.CancelSession(id) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	LiveChanged()
	w.WriteHeader(http.StatusOK)
}
