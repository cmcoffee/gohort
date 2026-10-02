package netgate

import "sync"

// InFlight bounds how many requests one key (a user, a token) may have running
// at once. A per-minute limit bounds how often work starts; it says nothing
// about a caller that starts a handful of long agent turns and holds them all
// open, which is the cost an external endpoint has to cap.
type InFlight struct {
	mu  sync.Mutex
	max int
	n   map[string]int
}

// NewInFlight returns a limiter allowing max concurrent requests per key. A
// max of zero or less allows everything.
func NewInFlight(max int) *InFlight {
	return &InFlight{max: max, n: map[string]int{}}
}

// Acquire takes a slot for key, returning the func that gives it back and
// true, or false when key already has max running. An empty key is not
// limited, since it cannot be told apart from any other caller.
func (f *InFlight) Acquire(key string) (release func(), ok bool) {
	if f == nil || f.max <= 0 || key == "" {
		return func() {}, true
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.n[key] >= f.max {
		return func() {}, false
	}
	f.n[key]++
	var once sync.Once
	return func() {
		once.Do(func() {
			f.mu.Lock()
			if f.n[key]--; f.n[key] <= 0 {
				delete(f.n, key)
			}
			f.mu.Unlock()
		})
	}, true
}
