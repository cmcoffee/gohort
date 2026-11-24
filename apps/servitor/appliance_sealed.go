package servitor

// A system's record holds what reaches it: an SSH password or private key, the
// sudo and root passwords, a repository's access token. It is written
// encrypted, whole, through saveAppliance, so those never sit in the database
// as plain bytes. kvlite marks each value as encrypted or not and reads either,
// so nothing that loads a system changes, and a record written before this
// still loads.
//
// Records written before this were plain. encryptStoredSystems writes each one
// again, encrypted, once per person. The plain bytes they replaced stay in the
// database file's freed pages until it is compacted, so the pass says to
// reclaim space when it re-wrote anything.

import (
	. "github.com/cmcoffee/gohort/core"
)

// systemsEncryptedKey marks, in a person's servitorMigrationsTable, that their
// systems were re-written encrypted.
const systemsEncryptedKey = "systems_encrypted"

// saveAppliance is the one way a system's record is written: encrypted. A
// plain Set on applianceTable would put its credentials in the file as they
// are (appliance_sealed_test.go refuses one).
func saveAppliance(udb Database, id string, a Appliance) {
	if udb == nil || id == "" {
		return
	}
	udb.CryptSet(applianceTable, id, a)
}

// encryptStoredSystems re-writes every person's systems encrypted, once each.
// Returns how many records it re-wrote.
func (T *Servitor) encryptStoredSystems() int {
	if T.DB == nil || AuthDB == nil {
		return 0
	}
	total := 0
	for _, u := range AuthListUsers(AuthDB()) {
		udb := UserDB(T.DB, u.Username)
		if udb == nil {
			continue
		}
		total += encryptUserSystems(udb)
	}
	if total > 0 {
		Notice("[servitor] %d system record(s) re-written encrypted. Their old unencrypted copies stay in the database file until it is compacted: run Administrator › Maintenance › Reclaim space.", total)
	}
	return total
}

// encryptUserSystems re-writes one person's systems encrypted, unless that
// was already done. Returns how many it re-wrote.
func encryptUserSystems(udb Database) int {
	var done bool
	if udb.Get(servitorMigrationsTable, systemsEncryptedKey, &done) && done {
		return 0
	}
	n := 0
	for _, id := range udb.Keys(applianceTable) {
		var a Appliance
		if !udb.Get(applianceTable, id, &a) {
			continue
		}
		saveAppliance(udb, id, a)
		n++
	}
	udb.Set(servitorMigrationsTable, systemsEncryptedKey, true)
	return n
}
