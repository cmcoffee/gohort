package customapps

// Shared collections: records every user of an app reads in common.
//
// An app's ordinary records are each user's own, even on a shared app, so a
// leaderboard, a lobby or a team list had nowhere to live. A shared
// collection lives once, in the OWNER's store. Any user who can open the app
// reads it (GET shared/<name>), and every script gets them all as the
// `shared` input; only an ACTION script writes one, by returning
// {"shared": {"<name>": [records]}} or {"shared_delete": {"<name>": [ids]}}.
// Actions run as the owner, so the owner's code decides what a player may
// write: a leaderboard script can refuse an impossible score. The page cannot
// write a shared record directly.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// maxSharedRecords bounds one collection, and maxSharedRecordBytes one record:
// an action any user can trigger must not be able to fill the owner's store.
const (
	maxSharedRecords     = 5000
	maxSharedRecordBytes = 32 << 10
)

func sharedTable(slug, name string) string { return "custom_shared:" + slug + ":" + name }

// sharedDeclared reports whether spec declares the collection name.
func sharedDeclared(spec AppSpec, name string) bool {
	for _, c := range spec.SharedCollections {
		if c == name {
			return true
		}
	}
	return false
}

// readShared is one collection's records, oldest first by created, then id.
func readShared(ownerDB Database, spec AppSpec, name string) []map[string]any {
	out := []map[string]any{}
	if ownerDB == nil {
		return out
	}
	tbl := sharedTable(spec.Slug, name)
	for _, k := range ownerDB.Keys(tbl) {
		var rec map[string]any
		if ownerDB.Get(tbl, k, &rec) {
			out = append(out, rec)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		ci, _ := out[i]["created"].(string)
		cj, _ := out[j]["created"].(string)
		if ci != cj {
			return ci < cj
		}
		ii, _ := out[i]["id"].(string)
		ij, _ := out[j]["id"].(string)
		return ii < ij
	})
	return out
}

// sharedInput is every declared collection as one JSON object, the `shared`
// input scripts read: {"<name>": [records]}.
func sharedInput(ownerDB Database, spec AppSpec) string {
	all := map[string]any{}
	for _, name := range spec.SharedCollections {
		all[name] = readShared(ownerDB, spec, name)
	}
	b, _ := json.Marshal(all)
	return string(b)
}

// sharedWriteMu serializes writes to one app's shared collections, so two
// players' actions landing together each see a whole collection.
var sharedWriteMu sync.Map // owner\x00slug -> *sync.Mutex

func sharedLock(owner, slug string) *sync.Mutex {
	m, _ := sharedWriteMu.LoadOrStore(owner+"\x00"+slug, &sync.Mutex{})
	return m.(*sync.Mutex)
}

// applySharedWrites upserts and deletes an action's shared records. Every
// collection must be declared and every record within the bounds, checked
// before anything is written, so a refused write changes nothing. Each record
// is stamped with who triggered it (by) and when, server-side: a script
// cannot claim a write was somebody else's.
func applySharedWrites(owner string, ownerDB Database, spec AppSpec, by string, writes map[string][]map[string]any, deletes map[string][]string) (int, error) {
	if len(writes) == 0 && len(deletes) == 0 {
		return 0, nil
	}
	if ownerDB == nil {
		return 0, fmt.Errorf("the app's store is not available")
	}
	for name := range writes {
		if !sharedDeclared(spec, name) {
			return 0, fmt.Errorf("the action wrote shared collection %q, which the app does not declare (shared_collections: %s)", name, strings.Join(spec.SharedCollections, ", "))
		}
	}
	for name := range deletes {
		if !sharedDeclared(spec, name) {
			return 0, fmt.Errorf("the action deleted from shared collection %q, which the app does not declare", name)
		}
	}
	mu := sharedLock(owner, spec.Slug)
	mu.Lock()
	defer mu.Unlock()
	for name, recs := range writes {
		tbl := sharedTable(spec.Slug, name)
		have := map[string]bool{}
		for _, k := range ownerDB.Keys(tbl) {
			have[k] = true
		}
		for _, id := range deletes[name] {
			delete(have, id)
		}
		for _, rec := range recs {
			if rec == nil {
				continue
			}
			if b, _ := json.Marshal(rec); len(b) > maxSharedRecordBytes {
				return 0, fmt.Errorf("a %q record is %d KiB: a shared record is at most %d KiB", name, len(b)>>10, maxSharedRecordBytes>>10)
			}
			if id, _ := rec["id"].(string); strings.TrimSpace(id) != "" {
				have[id] = true
			} else {
				have["\x00new"+fmt.Sprint(len(have))] = true
			}
		}
		if len(have) > maxSharedRecords {
			return 0, fmt.Errorf("shared collection %q would hold %d records: at most %d", name, len(have), maxSharedRecords)
		}
	}
	n := 0
	now := time.Now().UTC().Format(time.RFC3339)
	for name, ids := range deletes {
		tbl := sharedTable(spec.Slug, name)
		for _, id := range ids {
			ownerDB.Unset(tbl, strings.TrimSpace(id))
			n++
		}
	}
	for name, recs := range writes {
		tbl := sharedTable(spec.Slug, name)
		for _, rec := range recs {
			if rec == nil {
				continue
			}
			id, _ := rec["id"].(string)
			if strings.TrimSpace(id) == "" {
				id = newID()
				rec["id"] = id
			}
			var prior map[string]any
			if ownerDB.Get(tbl, id, &prior) && prior["created"] != nil {
				rec["created"] = prior["created"]
			} else {
				rec["created"] = now
			}
			rec["by"], rec["updated"] = by, now
			ownerDB.Set(tbl, id, rec)
			n++
		}
	}
	return n, nil
}

// handleShared is GET shared/<name>: one shared collection, for any user who
// can open the app. Read-only: writes go through the app's actions.
func (T *CustomApps) handleShared(w http.ResponseWriter, r *http.Request, ownerDB Database, spec AppSpec, name string) {
	if r.Method != http.MethodGet {
		http.Error(w, "shared collections are written by the app's actions: POST action/<name>, whose script returns {\"shared\": {...}}", http.StatusMethodNotAllowed)
		return
	}
	if !sharedDeclared(spec, name) {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, readShared(ownerDB, spec, name))
}
