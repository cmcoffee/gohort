package appscript

// The id a custom app's scripts know a person by.
//
// A script needs to tell players apart (one leaderboard entry each, one vote
// per person) and it needs that to hold across visits, but it does not need
// to know who anyone is. It used to get the username, which on a deployment
// that signs in by email is an email address, and every shared record was
// stamped with it in `by`. Any user of a shared app reads its shared
// collections, so a leaderboard handed every player's address to every other
// player, and a script that showed caller[:8] as a name printed most of it.
//
// CallerAlias is a keyed hash of (owner, app, user) instead: stable for one
// person in one app, different in every other app (so two apps cannot be
// joined to follow someone), and not reversible without the deployment's key.

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"

	. "github.com/cmcoffee/gohort/core"
)

const (
	aliasTable  = "customapps_alias"
	aliasPrefix = "p_"
	aliasHexLen = 16
)

var aliasKeys struct {
	mu   sync.Mutex
	db   Database
	key  []byte
	temp []byte // no database (a test, a tool run): one key for the process
}

// aliasKey is the deployment's alias key, made on first use and kept in
// RootDB so an alias survives a restart.
func aliasKey() []byte {
	aliasKeys.mu.Lock()
	defer aliasKeys.mu.Unlock()
	db := RootDB
	if db == nil {
		if aliasKeys.temp == nil {
			aliasKeys.temp = randomKey()
		}
		return aliasKeys.temp
	}
	if aliasKeys.db == db && aliasKeys.key != nil {
		return aliasKeys.key
	}
	var stored string
	var key []byte
	if db.Get(aliasTable, "key", &stored) {
		key, _ = hex.DecodeString(stored)
	}
	if len(key) == 0 {
		key = randomKey()
		db.Set(aliasTable, "key", hex.EncodeToString(key))
	}
	aliasKeys.db, aliasKeys.key = db, key
	return key
}

func randomKey() []byte {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return b
}

// CallerAlias is the id spec's scripts know uid by: "p_" and 16 hex digits.
// Empty for an empty uid.
func CallerAlias(spec AppSpec, uid string) string {
	uid = strings.TrimSpace(uid)
	if uid == "" {
		return ""
	}
	if IsCallerAlias(uid) {
		return uid
	}
	mac := hmac.New(sha256.New, aliasKey())
	mac.Write([]byte(spec.Owner + "\x00" + spec.Slug + "\x00" + uid))
	return aliasPrefix + hex.EncodeToString(mac.Sum(nil))[:aliasHexLen]
}

// IsCallerAlias reports whether s already has an alias's shape, so a record
// stamped after this change is not hashed a second time.
func IsCallerAlias(s string) bool {
	if len(s) != len(aliasPrefix)+aliasHexLen || !strings.HasPrefix(s, aliasPrefix) {
		return false
	}
	_, err := hex.DecodeString(s[len(aliasPrefix):])
	return err == nil
}
