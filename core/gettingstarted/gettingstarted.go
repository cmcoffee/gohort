// Package gettingstarted is the list of first things a person can do, shown
// on the dashboard until they have done them or put the card away.
//
// Each step belongs to whatever offers it: core adds the account's own (a
// name), an app adds its (an assistant, a collection), so the card never
// names an app it does not know. A step says who it is for, with the same
// access rule the page behind its link applies, so someone added for one
// app, or who only uses what is shared with them, is never offered a step
// they cannot take. Done is read from the account as it is, never ticked by
// hand, so a step done anywhere is done here.
//
// A leaf package rather than a file in core for the reason core/accountname
// is: core sits at its export ceiling.
package gettingstarted

import (
	"net/http"
	"sort"
	"sync"
)

// Step is one thing to do.
type Step struct {
	// ID names the step; registering an ID again replaces it.
	ID string
	// Title is the step, as an instruction: "Create your assistant".
	Title string
	// Help is a line on why, under the title.
	Help string
	// Link is where the step is done.
	Link string
	// Order places it among the others, lowest first.
	Order int
	// Applies reports whether the step is for this person at all. Nil is
	// everyone.
	Applies func(r *http.Request, user string) bool
	// Done reports whether they have done it.
	Done func(user string) bool
}

var (
	mu    sync.RWMutex
	steps = map[string]Step{}
)

// Register adds step, or replaces the one with its ID.
func Register(step Step) {
	if step.ID == "" || step.Done == nil {
		return
	}
	mu.Lock()
	steps[step.ID] = step
	mu.Unlock()
}

// Item is a step as one person sees it.
type Item struct {
	Step
	Finished bool
}

// For is the steps that apply to user, in order, each with whether it is done.
func For(r *http.Request, user string) []Item {
	mu.RLock()
	all := make([]Step, 0, len(steps))
	for _, s := range steps {
		all = append(all, s)
	}
	mu.RUnlock()
	sort.Slice(all, func(i, j int) bool {
		if all[i].Order != all[j].Order {
			return all[i].Order < all[j].Order
		}
		return all[i].ID < all[j].ID
	})
	var out []Item
	for _, s := range all {
		if s.Applies != nil && !s.Applies(r, user) {
			continue
		}
		out = append(out, Item{Step: s, Finished: s.Done(user)})
	}
	return out
}

// Remaining counts the items not yet done.
func Remaining(items []Item) int {
	n := 0
	for _, it := range items {
		if !it.Finished {
			n++
		}
	}
	return n
}
