package customapps

// Live updates: GET changes waits until the app's shared collections or the
// caller's own records change, then answers with their current versions.
//
// An app page had no way to hear that something changed: a leaderboard or a
// lobby another player wrote, or the user's own records from another tab or a
// scheduled action. It polled on a timer or showed stale data. This is a long
// poll, so it rides the frame's existing fetch relay with no new channel:
//
//	let v = {};
//	for (;;) { const r = await fetch('changes?shared=' + (v.shared || '') + '&records=' + (v.records || ''));
//	           v = await r.json(); refresh(); }
//
// A version is opaque: compare for difference, never order. Versions are
// held in memory and carry a per-process epoch, so a restart reads as a change
// and the page refetches rather than missing one.

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// changesWait is how long a request waits for a change before answering with
// the versions it was given, so the page asks again.
var changesWait = 25 * time.Second

var changesEpoch = fmt.Sprintf("%x", time.Now().UnixNano())

type changeKey struct {
	n  uint64
	ch chan struct{} // closed, and replaced, on each change
}

var changes = struct {
	sync.Mutex
	m map[string]*changeKey
}{m: map[string]*changeKey{}}

func sharedChangeKey(owner, slug string) string { return "shared\x00" + owner + "\x00" + slug }
func recordsChangeKey(owner, slug, user string) string {
	return "records\x00" + owner + "\x00" + slug + "\x00" + user
}

func changeEntry(key string) *changeKey {
	c := changes.m[key]
	if c == nil {
		c = &changeKey{ch: make(chan struct{})}
		changes.m[key] = c
	}
	return c
}

// noteChange marks key changed and wakes everything waiting on it.
func noteChange(key string) {
	changes.Lock()
	c := changeEntry(key)
	c.n++
	close(c.ch)
	c.ch = make(chan struct{})
	changes.Unlock()
}

// changeVersion is key's version now, and the channel its next change closes.
func changeVersion(key string) (string, chan struct{}) {
	changes.Lock()
	defer changes.Unlock()
	c := changeEntry(key)
	return fmt.Sprintf("%s.%d", changesEpoch, c.n), c.ch
}

// handleChanges is GET changes?shared=<v>&records=<v>: it answers at once when
// either version differs from the one given, else when either changes, else
// after changesWait, with {"shared": v, "records": v}.
func (T *CustomApps) handleChanges(w http.ResponseWriter, r *http.Request, owner, user, slug string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query()
	sk, rk := sharedChangeKey(owner, slug), recordsChangeKey(owner, slug, user)
	sv, sch := changeVersion(sk)
	rv, rch := changeVersion(rk)
	if sv == q.Get("shared") && rv == q.Get("records") {
		ctx, cancel := context.WithTimeout(r.Context(), changesWait)
		defer cancel()
		select {
		case <-sch:
		case <-rch:
		case <-ctx.Done():
		}
		sv, _ = changeVersion(sk)
		rv, _ = changeVersion(rk)
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, map[string]any{"shared": sv, "records": rv})
}
