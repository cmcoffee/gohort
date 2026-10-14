// How full a store may get, and keeping it there.
//
// Two limits, checked before work that writes into a store starts: the disk's
// free-space floor (core/diskguard), which protects the server sharing that
// disk, and the store's own cap (Store.MaxGB), which an admin sets so one
// store cannot take a disk other things need. Work past either is REFUSED
// with how full things are. Nothing is deleted to make room: that would
// remove what nobody chose to remove.
package filestore

import (
	"fmt"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/diskguard"
)

// usageTTL is how long a store's measured size is reused. Measuring walks the
// whole store, which for a store of captures is not something to do on every
// page render or every upload.
const usageTTL = time.Minute

var usageCache = struct {
	sync.Mutex
	at map[string]usageEntry
}{at: map[string]usageEntry{}}

type usageEntry struct {
	bytes int64
	when  time.Time
}

// usage is the store's total size, measured at most once a minute.
func (s Store) usage() int64 {
	usageCache.Lock()
	e, ok := usageCache.at[s.Slug]
	usageCache.Unlock()
	if ok && time.Since(e.when) < usageTTL {
		return e.bytes
	}
	n := folderBytes(s.Path)
	usageCache.Lock()
	usageCache.at[s.Slug] = usageEntry{bytes: n, when: time.Now()}
	usageCache.Unlock()
	return n
}

// wrote tells the size cache the store changed, so the next check measures it
// again instead of trusting a figure from before the write.
func (s Store) wrote() {
	usageCache.Lock()
	delete(usageCache.at, s.Slug)
	usageCache.Unlock()
}

// capBytes is the store's cap in bytes, 0 for none.
func (s Store) capBytes() int64 { return int64(s.MaxGB) << 30 }

// roomFor refuses a write of need bytes (0 when not known up front) that the
// disk's floor or the store's cap does not leave room for.
func (s Store) roomFor(need int64) error {
	if need < 0 {
		need = 0
	}
	if err := diskguard.Check(s.Path, uint64(need)); err != nil {
		return Error(s.Name + ": " + err.Error())
	}
	limit := s.capBytes()
	if limit <= 0 {
		return nil
	}
	used := s.usage()
	if used+need <= limit && used < limit {
		return nil
	}
	free := "remove cases from it"
	if s.Ephemeral {
		free = "wait for old cases to expire (it keeps a case " + fmt.Sprint(s.lifetime()) + " after its last change), remove cases from it"
	}
	if need == 0 {
		return Error(fmt.Sprintf("%s is full: it holds %s of its %s cap. Nothing was deleted to make room; %s, or raise its cap (Admin, Files).",
			s.Name, HumanSize(used), HumanSize(limit), free))
	}
	return Error(fmt.Sprintf("%s holds %s of its %s cap, and this needs %s more. Nothing was deleted to make room; %s, or raise its cap (Admin, Files).",
		s.Name, HumanSize(used), HumanSize(limit), HumanSize(need), free))
}

// spaceLeft is how much more may be written into the store before the floor
// or the cap is reached, or -1 when neither can say. An archive's unpacking
// budget is held to it.
func (s Store) spaceLeft() int64 {
	left := int64(-1)
	if sp, ok := diskguard.Of(s.Path); ok {
		floor := sp.Floor()
		left = 0
		if sp.Free > floor {
			left = int64(sp.Free - floor)
		}
	}
	if limit := s.capBytes(); limit > 0 {
		capLeft := limit - s.usage()
		if capLeft < 0 {
			capLeft = 0
		}
		if left < 0 || capLeft < left {
			left = capLeft
		}
	}
	return left
}
