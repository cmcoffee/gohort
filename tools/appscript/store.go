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

// RecordsTable is the table an app's own records live in.
func RecordsTable(slug string) string { return "custom_records:" + slug }

// ReadRecords is one person's records for an app, as every script gets them:
// oldest first by created, then by key. They came in key order, and a key is
// random hex, so records[-1], which every "the location I entered" script
// reads as the newest, was an arbitrary one: a weather app kept forecasting
// whichever saved city happened to sort last, and read as not saving the
// city just entered. A record with no created sorts first.
func ReadRecords(db Database, slug string) []map[string]any {
	type row struct {
		key string
		rec map[string]any
	}
	var rows []row
	if db != nil {
		tbl := RecordsTable(slug)
		for _, k := range db.Keys(tbl) {
			var rec map[string]any
			if db.Get(tbl, k, &rec) {
				rows = append(rows, row{k, rec})
			}
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		ci, _ := rows[i].rec["created"].(string)
		cj, _ := rows[j].rec["created"].(string)
		if ci != cj {
			return ci < cj
		}
		return rows[i].key < rows[j].key
	})
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.rec)
	}
	return out
}

// SharedTable is the table one shared collection lives in.
func SharedTable(slug, name string) string { return "custom_shared:" + slug + ":" + name }

// ReadShared is one collection's records, oldest first by created, then id.
// A by stamped before writers were aliased (a username) is read as the alias,
// so an old record neither shows an email nor stops matching its writer's
// caller.
func ReadShared(ownerDB Database, spec AppSpec, name string) []map[string]any {
	out := []map[string]any{}
	if ownerDB == nil {
		return out
	}
	tbl := SharedTable(spec.Slug, name)
	for _, k := range ownerDB.Keys(tbl) {
		var rec map[string]any
		if ownerDB.Get(tbl, k, &rec) {
			if by, ok := rec["by"].(string); ok && by != "" && !IsCallerAlias(by) {
				rec["by"] = CallerAlias(spec, by)
			}
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
		all[name] = ReadShared(ownerDB, spec, name)
	}
	b, _ := json.Marshal(all)
	return string(b)
}
