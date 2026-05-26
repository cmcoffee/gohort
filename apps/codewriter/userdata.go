package codewriter

import (
	"errors"

	. "github.com/cmcoffee/gohort/core"
)

// codeWriterUserData exposes snippets/values/contexts for the admin
// reassign/purge flow. Registered from RegisterRoutes once T.DB is wired.
type codeWriterUserData struct {
	agent *CodeWriterAgent
}

var codeWriterTables = []string{snippetTable, valueTable, contextTable}

// moveRecord decodes a gob-encoded value into the right concrete type
// for the given table, writes it to dst, then deletes it from src.
func codeWriterMove(src, dst Database, tbl, key string) {
	switch tbl {
	case snippetTable:
		var v SnippetRecord
		if src.Get(tbl, key, &v) {
			dst.Set(tbl, key, v)
			src.Unset(tbl, key)
		}
	case valueTable:
		var v SavedValue
		if src.Get(tbl, key, &v) {
			dst.Set(tbl, key, v)
			src.Unset(tbl, key)
		}
	case contextTable:
		var v ContextRecord
		if src.Get(tbl, key, &v) {
			dst.Set(tbl, key, v)
			src.Unset(tbl, key)
		}
	}
}

func (h *codeWriterUserData) AppName() string { return "codewriter" }

func (h *codeWriterUserData) Describe(uid string) UserDataSummary {
	sum := UserDataSummary{
		AppName: "codewriter",
		Counts:  map[string]int{},
		Actions: []string{"reassign", "purge"},
	}
	udb := UserDB(h.agent.DB, uid)
	if udb == nil {
		return sum
	}
	sum.Counts["snippets"] = udb.CountKeys(snippetTable)
	sum.Counts["values"] = udb.CountKeys(valueTable)
	sum.Counts["contexts"] = udb.CountKeys(contextTable)
	return sum
}

func (h *codeWriterUserData) Reassign(from, to string) error {
	src := UserDB(h.agent.DB, from)
	dst := UserDB(h.agent.DB, to)
	if src == nil || dst == nil {
		return errors.New("invalid user")
	}
	for _, tbl := range codeWriterTables {
		for _, k := range src.Keys(tbl) {
			codeWriterMove(src, dst, tbl, k)
		}
	}
	return nil
}

func (h *codeWriterUserData) Anonymize(uid string) error {
	return ErrUserDataActionNotSupported
}

func (h *codeWriterUserData) Purge(uid string) error {
	udb := UserDB(h.agent.DB, uid)
	if udb == nil {
		return errors.New("invalid user")
	}
	for _, tbl := range codeWriterTables {
		for _, k := range udb.Keys(tbl) {
			udb.Unset(tbl, k)
		}
	}
	return nil
}

// OrphanCounts returns counts for the legacy global tables — entries
// that were written before per-user scoping was enabled. The admin UI
// uses this to offer a one-time "adopt orphaned data" migration.
func (h *codeWriterUserData) OrphanCounts() map[string]int {
	if h.agent.DB == nil {
		return nil
	}
	return map[string]int{
		"snippets": h.agent.DB.CountKeys(snippetTable),
		"values":   h.agent.DB.CountKeys(valueTable),
		"contexts": h.agent.DB.CountKeys(contextTable),
	}
}

// AdoptOrphans moves all entries from the legacy global tables into the
// given user's sub-store. Safe to call multiple times.
func (h *codeWriterUserData) AdoptOrphans(uid string) error {
	dst := UserDB(h.agent.DB, uid)
	if dst == nil {
		return errors.New("invalid user")
	}
	for _, tbl := range codeWriterTables {
		for _, k := range h.agent.DB.Keys(tbl) {
			codeWriterMove(h.agent.DB, dst, tbl, k)
		}
	}
	return nil
}
