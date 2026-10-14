// Package pathhold records which folders work is using right now, so that a
// sweep never removes one out from under it.
//
// A temporary store deletes a case folder once nothing in it has changed for
// its lifetime. "Nothing changed" is not "nothing is using it": a command
// reading a folder writes nothing there, and an extractor writing deep inside
// one does not refresh the age a sweep reads. Whatever reads or writes a store
// folder holds it for as long as it runs, and a sweep skips anything held, or
// inside or above something held.
//
// In-process only: work that runs in this server holds here, and a server
// restart, which ends that work, ends its holds too.
package pathhold

import (
	"path/filepath"
	"strings"
	"sync"
)

var (
	mu    sync.Mutex
	holds = map[string]int{}
)

// Hold marks path in use until release is called. Holding a path twice is
// fine; it stays held until both are released. release is safe to call more
// than once.
func Hold(path string) (release func()) {
	p := clean(path)
	if p == "" {
		return func() {}
	}
	mu.Lock()
	holds[p]++
	mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			mu.Lock()
			defer mu.Unlock()
			if holds[p]--; holds[p] <= 0 {
				delete(holds, p)
			}
		})
	}
}

// HoldAll holds every path, released together.
func HoldAll(paths ...string) (release func()) {
	var rs []func()
	for _, p := range paths {
		rs = append(rs, Hold(p))
	}
	return func() {
		for _, r := range rs {
			r()
		}
	}
}

// InUse reports whether path is held, or anything inside it is, or a folder
// it is inside is. Removing path would pull the ground from under any of
// those.
func InUse(path string) bool {
	p := clean(path)
	if p == "" {
		return false
	}
	mu.Lock()
	defer mu.Unlock()
	for h := range holds {
		if h == p || within(h, p) || within(p, h) {
			return true
		}
	}
	return false
}

func clean(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	return filepath.Clean(path)
}

// within reports whether p is strictly inside dir.
func within(p, dir string) bool {
	return strings.HasPrefix(p, dir+string(filepath.Separator))
}
