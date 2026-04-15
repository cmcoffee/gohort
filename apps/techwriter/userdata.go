package techwriter

import (
	"errors"

	. "github.com/cmcoffee/gohort/core"
)

// techWriterUserData exposes techwriter article history and personas for
// the admin reassign/purge flow. Registered once T.DB is wired.
type techWriterUserData struct {
	agent *TechWriterAgent
}

var techWriterTables = []string{HistoryTable, personaTable}

func techWriterMove(src, dst Database, tbl, key string) {
	switch tbl {
	case HistoryTable:
		var v ArticleRecord
		if src.Get(tbl, key, &v) {
			dst.Set(tbl, key, v)
			src.Unset(tbl, key)
		}
	case personaTable:
		var v Persona
		if src.Get(tbl, key, &v) {
			dst.Set(tbl, key, v)
			src.Unset(tbl, key)
		}
	}
}

func (h *techWriterUserData) AppName() string { return "techwriter" }

func (h *techWriterUserData) Describe(uid string) UserDataSummary {
	sum := UserDataSummary{
		AppName: "techwriter",
		Counts:  map[string]int{},
		Actions: []string{"reassign", "purge"},
	}
	udb := UserDB(h.agent.DB, uid)
	if udb == nil {
		return sum
	}
	sum.Counts["articles"] = udb.CountKeys(HistoryTable)
	sum.Counts["personas"] = udb.CountKeys(personaTable)
	return sum
}

func (h *techWriterUserData) Reassign(from, to string) error {
	src := UserDB(h.agent.DB, from)
	dst := UserDB(h.agent.DB, to)
	if src == nil || dst == nil {
		return errors.New("invalid user")
	}
	for _, tbl := range techWriterTables {
		for _, k := range src.Keys(tbl) {
			techWriterMove(src, dst, tbl, k)
		}
	}
	return nil
}

func (h *techWriterUserData) Anonymize(uid string) error {
	return ErrUserDataActionNotSupported
}

func (h *techWriterUserData) Purge(uid string) error {
	udb := UserDB(h.agent.DB, uid)
	if udb == nil {
		return errors.New("invalid user")
	}
	for _, tbl := range techWriterTables {
		for _, k := range udb.Keys(tbl) {
			udb.Unset(tbl, k)
		}
	}
	return nil
}

// OrphanCounts returns the global (pre-scoping) table sizes.
func (h *techWriterUserData) OrphanCounts() map[string]int {
	if h.agent.DB == nil {
		return nil
	}
	return map[string]int{
		"articles": h.agent.DB.CountKeys(HistoryTable),
		"personas": h.agent.DB.CountKeys(personaTable),
	}
}

// AdoptOrphans moves all legacy global-table entries into the given user's
// sub-store. Idempotent.
func (h *techWriterUserData) AdoptOrphans(uid string) error {
	dst := UserDB(h.agent.DB, uid)
	if dst == nil {
		return errors.New("invalid user")
	}
	for _, tbl := range techWriterTables {
		for _, k := range h.agent.DB.Keys(tbl) {
			techWriterMove(h.agent.DB, dst, tbl, k)
		}
	}
	return nil
}
