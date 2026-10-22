// Package changes is gohort's "something changed" signal: named topics, each
// with a version, that a page waits on instead of polling for what moved.
//
// A writer that knows a thing changed notes its topic; a page asks with the
// versions it last saw and is answered the moment one differs, or after a
// while with the same ones, and asks again. That is a long poll, so it needs
// no new transport, and a version cannot miss a change the way a bare wake
// can: a page that was between requests when the change happened sees a
// different version on its next ask.
//
// A version is opaque: compare for difference, never order. Versions live in
// memory and carry a per-process epoch, so a restart reads as a change and a
// page refetches rather than trusting what it had.
//
// A topic says that something changed, never what: a page refetches what it
// shows through its usual endpoints, with their usual access checks. Topics
// that belong to one person are named with ForUser, so one person's activity
// never moves another's versions.
package changes

import (
	"context"
	"fmt"
	"sync"
	"time"
)

var epoch = fmt.Sprintf("%x", time.Now().UnixNano())

type topic struct {
	n  uint64
	ch chan struct{} // closed, and replaced, on each change
}

var topics = struct {
	sync.Mutex
	m map[string]*topic
}{m: map[string]*topic{}}

func entry(name string) *topic {
	t := topics.m[name]
	if t == nil {
		t = &topic{ch: make(chan struct{})}
		topics.m[name] = t
	}
	return t
}

// ForUser is the name of topic for one user: "sessions" for alice.
func ForUser(topic, user string) string { return topic + "\x00" + user }

// Note marks a topic changed and wakes everything waiting on it.
func Note(name string) {
	if name == "" {
		return
	}
	topics.Lock()
	t := entry(name)
	t.n++
	close(t.ch)
	t.ch = make(chan struct{})
	topics.Unlock()
}

// Version is a topic's version now, and the channel its next change closes.
func Version(name string) (string, <-chan struct{}) {
	topics.Lock()
	defer topics.Unlock()
	t := entry(name)
	return fmt.Sprintf("%s.%d", epoch, t.n), t.ch
}

// Wait answers with the current version of each topic in seen (by topic
// name, the version the caller last had): at once when one already differs,
// else as soon as one changes, else after max or when ctx ends, with them as
// they stand.
func Wait(ctx context.Context, seen map[string]string, max time.Duration) map[string]string {
	now := func() (map[string]string, []<-chan struct{}) {
		out := make(map[string]string, len(seen))
		chs := make([]<-chan struct{}, 0, len(seen))
		for name := range seen {
			v, ch := Version(name)
			out[name] = v
			chs = append(chs, ch)
		}
		return out, chs
	}
	cur, chs := now()
	for name, v := range cur {
		if seen[name] != v {
			return cur
		}
	}
	ctx, cancel := context.WithTimeout(ctx, max)
	defer cancel()
	woke := make(chan struct{}, 1)
	for _, ch := range chs {
		go func(ch <-chan struct{}) {
			select {
			case <-ch:
				select {
				case woke <- struct{}{}:
				default:
				}
			case <-ctx.Done():
			}
		}(ch)
	}
	select {
	case <-woke:
	case <-ctx.Done():
	}
	cur, _ = now()
	return cur
}
