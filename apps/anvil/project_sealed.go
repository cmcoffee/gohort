package anvil

// A project's record holds a repository's clone token and its push token.
// saveProject writes it encrypted, whole, so neither sits in the database as
// plain bytes; kvlite reads encrypted and plain values alike, so loading is
// unchanged and an older record still loads.
//
// Records written before this were plain. encryptStoredProjects writes each
// one again, encrypted, once per store. The plain bytes they replaced stay in
// the database file's freed pages until it is compacted.

import (
	. "github.com/cmcoffee/gohort/core"
)

// anvilMigrationsTable keeps which one-time passes have run on a store.
const anvilMigrationsTable = "anvil_migrations"

// projectsEncryptedKey marks a store's projects as re-written encrypted.
const projectsEncryptedKey = "projects_encrypted"

// encryptStoredProjects re-writes the projects in the shared store and in
// each person's, encrypted, once each. Returns how many records it re-wrote.
func (T *Anvil) encryptStoredProjects() int {
	if T.DB == nil {
		return 0
	}
	total := encryptProjectsIn(T.DB)
	if AuthDB != nil {
		for _, u := range AuthListUsers(AuthDB()) {
			if udb := UserDB(T.DB, u.Username); udb != nil {
				total += encryptProjectsIn(udb)
			}
		}
	}
	if total > 0 {
		Notice("[anvil] %d project record(s) re-written encrypted. Their old unencrypted copies stay in the database file until it is compacted: run Administrator › Maintenance › Reclaim space.", total)
	}
	return total
}

// encryptProjectsIn re-writes one store's projects encrypted, unless that was
// already done. Returns how many it re-wrote.
func encryptProjectsIn(db Database) int {
	var done bool
	if db.Get(anvilMigrationsTable, projectsEncryptedKey, &done) && done {
		return 0
	}
	n := 0
	for _, id := range db.Keys(projectTable) {
		var p Project
		if !db.Get(projectTable, id, &p) {
			continue
		}
		db.CryptSet(projectTable, id, p)
		n++
	}
	db.Set(anvilMigrationsTable, projectsEncryptedKey, true)
	return n
}
