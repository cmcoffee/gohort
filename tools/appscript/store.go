package appscript

// Where a custom app's data lives, for the code outside the host that has to
// read it. The authoring check runs an app's scripts against its stored
// records and shared collections; it used to read them from RootDB, while the
// host keeps them in its own bucket, so every check saw an empty store and
// ran against the sample or nothing. Both now resolve the store here.

import (
	"encoding/json"
	"sort"

	. "github.com/cmcoffee/gohort/core"
)

// StoreName is the custom-apps host's store, the bucket of the global
// database its records live in. The host's Name returns it, so the two
// cannot drift.
const StoreName = "customapps"

// RecordBase is uid's store for spec, as the host resolves it: the app's own
// database when it has one, else uid's part of the host's bucket. Shared
// collections live in the owner's, RecordBase(spec, spec.Owner). Nil when
// the databases are not open.
func RecordBase(spec AppSpec, uid string) Database {
	if spec.PrivateDB {
		if db := OpenCustomAppDB(spec.Owner, spec.Slug); db != nil {
			return UserDB(db, uid)
		}
	}
	if RootDB == nil {
		return nil
	}
	return UserDB(RootDB.Bucket(StoreName), uid)
}

// SharedTable is the table one shared collection lives in.
func SharedTable(slug, name string) string { return "custom_shared:" + slug + ":" + name }

// ReadShared is one collection's records, oldest first by created, then id.
func ReadShared(ownerDB Database, slug, name string) []map[string]any {
	out := []map[string]any{}
	if ownerDB == nil {
		return out
	}
	tbl := SharedTable(slug, name)
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

// SharedInput is every declared collection as one JSON object, the `shared`
// input scripts read: {"<name>": [records]}.
func SharedInput(ownerDB Database, spec AppSpec) string {
	all := map[string]any{}
	for _, name := range spec.SharedCollections {
		all[name] = ReadShared(ownerDB, spec.Slug, name)
	}
	b, _ := json.Marshal(all)
	return string(b)
}
