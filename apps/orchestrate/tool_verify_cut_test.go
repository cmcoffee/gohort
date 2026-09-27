package orchestrate

import (
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// Cutting turns from a thread drops what they recorded about the tools they
// edited, and keeps what came before: a retried turn was held by the build
// check to a tool only the removed turn had touched.
func TestACutTurnTakesItsToolRecordWithIt(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	recordToolVerify(db, "s1", "music_gen", true, "")
	time.Sleep(5 * time.Millisecond)
	cut := time.Now()
	time.Sleep(5 * time.Millisecond)
	recordToolVerify(db, "s1", "get_meme", false, "edited, never tested")
	forgetToolVerificationsSince(db, "s1", cut)
	recs := loadToolVerifications(db, "s1")
	if len(recs) != 1 || recs[0].Tool != "music_gen" {
		t.Fatalf("only the record from before the cut should stay, got %+v", recs)
	}
	forgetToolVerificationsSince(db, "s1", time.Time{})
	if len(loadToolVerifications(db, "s1")) != 1 {
		t.Error("no cut time, nothing dropped")
	}
}
