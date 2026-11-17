package admin

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// slowParts are the parts of one section builder that took long, so a slow
// page's log line can be followed into the builder it names: "slow builders:
// capabilities 1.352s" said which builder and nothing about which of its ten
// parts, and the cause depends on the machine (a probe, a peer, a file).
type slowParts struct {
	mu    sync.Mutex
	parts []string
}

// slowPartMin is how long a part takes before it is named.
const slowPartMin = 100 * time.Millisecond

// timed runs one part of a builder, noting it when it is slow.
func timed[T any](s *slowParts, name string, build func() T) T {
	started := time.Now()
	v := build()
	if d := time.Since(started); d >= slowPartMin {
		s.mu.Lock()
		s.parts = append(s.parts, fmt.Sprintf("%s %s", name, d.Round(time.Millisecond)))
		s.mu.Unlock()
	}
	return v
}

// log says which parts of builder were slow, when any were.
func (s *slowParts) log(builder string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.parts) == 0 {
		return
	}
	sort.Strings(s.parts)
	Log("[admin] the %s sections' slow parts: %s", builder, strings.Join(s.parts, ", "))
}
