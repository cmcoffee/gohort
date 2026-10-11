// Package docsink is where a finished document can be saved.
//
// An app that keeps documents registers itself as a sink (Scribe: "Save to
// Scribe"); a surface that produces a document (the artifact pane) offers the
// user every sink they can reach. Neither side names the other: core serves
// the list and the save, the pane draws one button per sink, and the app
// decides what saving means for it.
//
// A leaf: it imports nothing from core, so core and apps can both import it.
package docsink

import (
	"net/http"
	"sort"
	"sync"
)

// Sink is one place a document can be saved.
type Sink struct {
	// Name is a stable id ("scribe"), what the save request names.
	Name string
	// Label is the button text ("Save to Scribe").
	Label string
	// Available reports whether this request's user may use it (app access,
	// the app switched on). Nil means everyone signed in.
	Available func(r *http.Request) bool
	// Save stores the document for user and returns where it now lives (a
	// URL the user can open) and how to call it in a message (its title as
	// stored). title may be empty; the sink picks one.
	Save func(user, title, html string) (url, stored string, err error)
}

var (
	mu    sync.Mutex
	sinks = map[string]Sink{}
)

// Register adds or replaces a sink by name. Call from an app's setup.
func Register(s Sink) {
	if s.Name == "" || s.Save == nil {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	sinks[s.Name] = s
}

// For lists the sinks this request's user may use, by label.
func For(r *http.Request) []Sink {
	mu.Lock()
	all := make([]Sink, 0, len(sinks))
	for _, s := range sinks {
		all = append(all, s)
	}
	mu.Unlock()
	out := all[:0]
	for _, s := range all {
		if s.Available == nil || s.Available(r) {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	return out
}

// Find returns the named sink if this request's user may use it.
func Find(r *http.Request, name string) (Sink, bool) {
	for _, s := range For(r) {
		if s.Name == name {
			return s, true
		}
	}
	return Sink{}, false
}
