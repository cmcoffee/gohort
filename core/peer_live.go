// Peer work, in the place local background work already appears.
//
// A borrowed model runs on this machine's GPU and competes with everything the
// operator is doing on it. Until now that was invisible: a turn took longer,
// the log said nothing, and the only honest description of the experience was
// "it got slow and I don't know why". The scheduler knew — it had been queueing
// the work and counting it per caller since peer requests started taking slots —
// but nothing read that back out.
//
// So this is a READER, not a new record. Every number here comes from the
// schedulers that are actually doing the queueing, which is the point: a
// separate counter would be a second source of truth about how busy the GPU is,
// and the two would disagree the first time anything was cancelled mid-flight.
//
// It reports peers only. Local work already has its own live entries from the
// apps that started it, and duplicating those here would double every row in
// the ribbon.
package core

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

func init() {
	RegisterLiveProvider(peerLiveEntries)
}

// peerLivePrefix is the caller label peer work is scheduled under. Set at the
// point of acquisition (see peer_serve.go and peer_models.go); read here.
const peerLivePrefix = "peer:"

// peerLiveOrder places these after an app's own rows in the ribbon. Someone
// else's work borrowed on your hardware is context for what you are doing, not
// the thing you came to look at.
const peerLiveOrder = 90

// peerLiveEntries reports what peers are currently doing on this instance.
func peerLiveEntries() []LiveEntry {
	var out []LiveEntry
	// Both model schedulers: a deployment runs one, and asking the idle one
	// costs a nil check.
	out = append(out, peerRowsFrom(LlamacppSchedulerStats(), peerModelWork)...)
	out = append(out, peerRowsFrom(OllamaSchedulerStats(), peerModelWork)...)
	out = append(out, peerRowsFrom(ImageSchedulerStats(), peerImageWork)...)
	// Stable order so the ribbon does not reshuffle between polls — a row that
	// moves reads as a new row, and the point of this is to be glanceable.
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// peerWork is what one scheduler lends a peer, in the words of its rows. A
// peer borrows that one thing and nothing else: its apps, agents and their
// tools all run on the peer, so the row names the peer's app as remote and
// says only the model (or the renderer) runs here. Read as "running a model,
// for its app.orchestrate.worker" it sounded like this machine's orchestrate.
type peerWork struct {
	uses    string // "this machine's model"
	doing   string // a row with nothing more to say: "using this machine's model"
	waiting string // "waiting for this machine's model"
	here    string // what runs here: "the model"
	model   bool   // its calls name a tier, an app and a route (notePeerCall)
}

var (
	peerModelWork = peerWork{uses: "this machine's model", doing: "using this machine's model", waiting: "waiting for this machine's model", here: "the model", model: true}
	peerImageWork = peerWork{doing: "rendering an image", waiting: "waiting to render an image", here: "the render"}
)

// peerRowsFrom turns one scheduler's per-caller counts into live rows.
//
// In-flight and queued are separate rows on purpose. "A peer is using the GPU"
// and "a peer is waiting behind you" are different facts, and the second is the
// one that explains why a queue is deep without implying the machine is busy
// with someone else's work right now.
func peerRowsFrom(st OllamaSchedStats, work peerWork) []LiveEntry {
	var out []LiveEntry
	for caller, n := range st.Callers {
		if n <= 0 || !strings.HasPrefix(caller, peerLivePrefix) {
			continue
		}
		doing, status := work.doing, "only "+work.here+" runs here"
		if work.model {
			c := peerCallsFor(caller)
			doing = peerModelDoing(work, c)
			if c.routes != "" {
				status += "; its calls: " + c.routes
			}
		}
		out = append(out, LiveEntry{
			ID:         "peerwork:" + work.doing + ":" + caller,
			Label:      peerRowLabel(caller, doing, n),
			Background: true, // it started without the viewer, by definition
			// Nothing here was typed by anybody: the peer's own key label and a
			// fixed description of what it is doing. Masked, this row says
			// "Peers - another user", which withholds the only thing it is for.
			PublicLabel: true,
			App:         "Peers",
			Status:      status,
			Order:       peerLiveOrder,
		})
	}
	for caller, n := range st.Queued {
		if n <= 0 || !strings.HasPrefix(caller, peerLivePrefix) {
			continue
		}
		out = append(out, LiveEntry{
			ID:          "peerqueue:" + work.doing + ":" + caller,
			Label:       fmt.Sprintf("%s: %d %s", peerDisplayName(caller), n, work.waiting),
			Queued:      true,
			Background:  true,
			PublicLabel: true,
			App:         "Peers",
			Order:       peerLiveOrder,
		})
	}
	return out
}

// peerModelDoing is what a peer's model calls are doing, in the row's words:
// "remote scribe is using this machine's worker model".
func peerModelDoing(work peerWork, c peerCallsNow) string {
	uses := work.uses
	if c.tiers != "" {
		uses = "this machine's " + c.tiers
	}
	switch len(c.apps) {
	case 0:
		return "using " + uses
	case 1:
		return "remote " + c.apps[0] + " is using " + uses
	}
	return "remote " + strings.Join(c.apps, " and ") + " are using " + uses
}

// peerRowLabel names the peer and what it is doing, pluralized honestly.
func peerRowLabel(caller, doing string, n int) string {
	name := peerDisplayName(caller)
	if n > 1 {
		return fmt.Sprintf("%s: %s (%d at once)", name, doing, n)
	}
	return fmt.Sprintf("%s: %s", name, doing)
}

// peerDisplayName renders the caller label as the operator knows the peer.
//
// The key's LABEL, not its id: the label is what was typed when the key was
// minted precisely so a person could recognize the far side later, and a row
// reading "peer:3f2a…" answers none of the question it exists to answer.
func peerDisplayName(caller string) string {
	name := strings.TrimSpace(strings.TrimPrefix(caller, peerLivePrefix))
	if name == "" {
		return "A peer"
	}
	return "Peer " + name
}

// peerCallRoutes counts the borrowed model calls in flight by peer: the tier
// of this machine's each runs on, and the app and route the peer named, so a
// peer's row says which. Kept beside the scheduler's per-caller counts, not in
// them: a caller per purpose would change who the scheduler's round-robin is
// fair between.
var peerCallRoutes = struct {
	sync.Mutex
	m map[string]map[peerCall]int
}{m: map[string]map[peerCall]int{}}

// peerCall is one kind of borrowed call: the tier it runs on here ("worker",
// "lead"), and the app and route the peer named.
type peerCall struct{ tier, app, route string }

// notePeerCall counts one call until the returned func is called.
func notePeerCall(caller string, c peerCall) func() {
	c.tier = strings.ToLower(strings.TrimSpace(c.tier))
	peerCallRoutes.Lock()
	if peerCallRoutes.m[caller] == nil {
		peerCallRoutes.m[caller] = map[peerCall]int{}
	}
	peerCallRoutes.m[caller][c]++
	peerCallRoutes.Unlock()
	return func() {
		peerCallRoutes.Lock()
		defer peerCallRoutes.Unlock()
		if n := peerCallRoutes.m[caller][c] - 1; n > 0 {
			peerCallRoutes.m[caller][c] = n
		} else {
			delete(peerCallRoutes.m[caller], c)
		}
		if len(peerCallRoutes.m[caller]) == 0 {
			delete(peerCallRoutes.m, caller)
		}
	}
}

// peerCallsNow is what a peer's calls in flight are: the tiers they run on
// ("worker model"), the apps that asked, and their routes ("history.fold (2),
// unnamed"). The apps and routes are the PEER's, never this machine's.
type peerCallsNow struct {
	tiers  string
	apps   []string
	routes string
}

// peerCallsFor reads what caller's calls in flight are; zero when none.
func peerCallsFor(caller string) peerCallsNow {
	peerCallRoutes.Lock()
	defer peerCallRoutes.Unlock()
	var out peerCallsNow
	calls := peerCallRoutes.m[caller]
	if len(calls) == 0 {
		return out
	}
	routes := map[string]int{}
	seenTier, seenApp := map[string]bool{}, map[string]bool{}
	var ts []string
	for c, n := range calls {
		route := c.route
		if route == "" {
			route = "unnamed"
		}
		routes[route] += n
		if c.tier != "" && !seenTier[c.tier] {
			seenTier[c.tier] = true
			ts = append(ts, c.tier)
		}
		if c.app != "" && !seenApp[c.app] {
			seenApp[c.app] = true
			out.apps = append(out.apps, c.app)
		}
	}
	sort.Strings(out.apps)
	sort.Sort(sort.Reverse(sort.StringSlice(ts))) // worker before lead
	switch len(ts) {
	case 0:
	case 1:
		out.tiers = ts[0] + " model"
	default:
		out.tiers = strings.Join(ts, " and ") + " models"
	}
	list := make([]string, 0, len(routes))
	for r := range routes {
		list = append(list, r)
	}
	sort.Strings(list)
	for i, r := range list {
		if n := routes[r]; n > 1 {
			list[i] = fmt.Sprintf("%s (%d)", r, n)
		}
	}
	out.routes = strings.Join(list, ", ")
	return out
}
