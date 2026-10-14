// Package diskguard keeps writes from filling the disk they land on.
//
// Every place gohort writes something big (a command unpacking a capture, a
// pull copying a case between stores, an upload) shares one volume with the
// server's own database and logs. A runaway write that fills it does not fail
// alone: the database stops taking writes, the logs stop, and the whole
// deployment goes down with the one job. So there is a FLOOR: free space the
// writes may not take, checked before big work starts and while it runs.
//
// The floor is a fixed amount by default. A share of the volume is there for
// an admin who wants one, and is off until set: on a large disk a percentage
// holds back far more than the server needs (5% of 2 TB is 100 GB).
package diskguard

import (
	"fmt"

	. "github.com/cmcoffee/gohort/core"
)

func init() {
	RegisterTunable(TunableSpec{
		Key: "tune_disk_floor_gb", Category: "Limits",
		Label:  "Keep free on disk (GB)",
		Help:   "Large writes stop before the free space on their disk drops below this.",
		Detail: "Covers commands run by tools, copies between file stores and uploads. It protects the server's own database and logs, which share the disk. If the percentage below is also set, the larger of the two applies.",
		Kind:   KindInt, Default: 10, Min: 1, Max: 10000,
	})
	RegisterTunable(TunableSpec{
		Key: "tune_disk_floor_pct", Category: "Limits",
		Label:  "Keep free on disk (%)",
		Help:   "Optional: also keep this share of the disk free. 0 is off.",
		Detail: "Off by default, because on a large disk a share holds back far more than needed (5% of 2 TB is 100 GB). When set, the larger of this and the GB amount applies.",
		Kind:   KindInt, Default: 0, Min: 0, Max: 50,
	})
}

// Space is what a volume has.
type Space struct {
	Free  uint64 // bytes available to this process
	Total uint64 // bytes on the volume
}

// Floor is the free space writes must leave on this volume.
func (s Space) Floor() uint64 {
	gb := uint64(TuneInt("tune_disk_floor_gb")) << 30
	pct := s.Total / 100 * uint64(TuneInt("tune_disk_floor_pct"))
	if pct > gb {
		return pct
	}
	return gb
}

// Of reports the space on the volume holding path. ok is false where the
// platform cannot say, and then nothing is refused on its account.
func Of(path string) (Space, bool) {
	return statSpace(path)
}

// Check refuses a write of need bytes into path when it would leave less than
// the floor free. need may be 0, to ask whether there is room for anything.
// The error says how much is free and what the floor is, for a person.
func Check(path string, need uint64) error {
	sp, ok := Of(path)
	if !ok {
		return nil
	}
	floor := sp.Floor()
	if sp.Free >= need && sp.Free-need >= floor {
		return nil
	}
	if need == 0 {
		return fmt.Errorf("the disk holding %s has %s free, below the %s it keeps free for the server; free space there or lower the floor (Admin, Limits)",
			path, HumanSize(int64(sp.Free)), HumanSize(int64(floor)))
	}
	return fmt.Errorf("the disk holding %s has %s free; writing %s would leave less than the %s it keeps free for the server; free space there or lower the floor (Admin, Limits)",
		path, HumanSize(int64(sp.Free)), HumanSize(int64(need)), HumanSize(int64(floor)))
}
