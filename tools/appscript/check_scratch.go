package appscript

// A page check's scratch records (docs/design/app-checks.md). A page check
// drives the app's real page, and pressing Add would write a record, so its
// browser carries a cookie naming a store made for the check: the app's record
// endpoints and the scripts they run use it in place of anyone's records, and
// it is dropped when the check ends. An unknown or ended token is refused by
// the app host, never read as no check at all.

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// CheckScratchCookie is the cookie a page check's browser carries.
const CheckScratchCookie = "gohort_app_check"

// CheckScratch is one page check's store, for one app.
type CheckScratch struct {
	DB    Database
	Owner string
	Slug  string
}

var (
	scratchMu sync.Mutex
	scratches = map[string]CheckScratch{}
)

// BeginCheckScratch makes a page check's store for owner's app slug, seeded
// with records (keyed by recordKey when a record carries it) and with shared
// ({collection: records}, SeedShared), and returns the token its browser
// carries and the call that drops it.
func BeginCheckScratch(owner, slug, recordKey string, records []map[string]any, shared map[string][]map[string]any) (token string, end func()) {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	token = hex.EncodeToString(b)
	db := &DBase{Store: kvlite.MemStore()}
	tbl := RecordsTable(slug)
	for i, rec := range records {
		key := fmt.Sprintf("check-%03d", i+1)
		if recordKey != "" {
			if v, ok := rec[recordKey]; ok && fmt.Sprint(v) != "" {
				key = fmt.Sprint(v)
			}
		}
		db.Set(tbl, key, rec)
	}
	SeedShared(db, slug, shared)
	scratchMu.Lock()
	scratches[token] = CheckScratch{DB: db, Owner: owner, Slug: slug}
	scratchMu.Unlock()
	return token, func() {
		scratchMu.Lock()
		delete(scratches, token)
		scratchMu.Unlock()
	}
}

// CheckScratchFor finds a live page check's store by its token.
func CheckScratchFor(token string) (CheckScratch, bool) {
	scratchMu.Lock()
	defer scratchMu.Unlock()
	sc, ok := scratches[token]
	return sc, ok
}

// SeedShared puts a check's shared records into its scratch store db, each
// collection where the app's shared records live. A record without an id is
// given one, "<collection>-<n>", as a real shared write would give it one, so
// a script looking a record up by id finds it.
func SeedShared(db Database, slug string, shared map[string][]map[string]any) {
	for name, recs := range shared {
		tbl := SharedTable(slug, name)
		for i, rec := range recs {
			if rec == nil {
				continue
			}
			id, _ := rec["id"].(string)
			if id == "" {
				if v, ok := rec["id"]; ok && v != nil {
					id = fmt.Sprint(v)
				} else {
					id = fmt.Sprintf("%s-%d", name, i+1)
				}
				rec["id"] = id
			}
			db.Set(tbl, id, rec)
		}
	}
}
