package core

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/cmcoffee/gohort/core/peershare"
	"github.com/cmcoffee/gohort/core/promotion"
)

// ErrUserDataActionNotSupported should be returned by a UserDataHandler when
// a requested action (reassign, anonymize, purge) does not apply to the data
// it manages.
var ErrUserDataActionNotSupported = errors.New("action not supported")

// UserDataSummary describes how much data an app holds for a user and
// which admin actions are applicable when the user account is removed.
type UserDataSummary struct {
	AppName string         `json:"app"`
	Counts  map[string]int `json:"counts"`
	Actions []string       `json:"actions"` // subset of "reassign", "anonymize", "purge"
}

// UserDataHandler lets an app expose its per-user data to the admin
// reassign/anonymize/purge flow. Return ErrUserDataActionNotSupported for
// any action the app does not offer.
type UserDataHandler interface {
	AppName() string
	Describe(uid string) UserDataSummary
	Reassign(from, to string) error
	Anonymize(uid string) error
	Purge(uid string) error
}

// UserDataReassignChecker is a UserDataHandler that can say, before anything
// moves, why moving a user's data to another would be refused. Moves run one
// app at a time, so a refusal found only when its app's turn came left the
// apps before it moved and the rest not: a half-moved account.
type UserDataReassignChecker interface {
	CheckReassign(from, to string) error
}

// CheckUserDataReassign asks every handler that can check whether moving
// from's data to to would be refused, and returns each refusal as
// "<app>: <why>". Empty means the move can go ahead.
func CheckUserDataReassign(from, to string) []string {
	var out []string
	for _, h := range RegisteredUserDataHandlers() {
		c, ok := h.(UserDataReassignChecker)
		if !ok {
			continue
		}
		if err := c.CheckReassign(from, to); err != nil {
			out = append(out, h.AppName()+": "+err.Error())
		}
	}
	return out
}

var (
	userDataMu       sync.Mutex
	userDataHandlers []UserDataHandler
)

// RegisterUserDataHandler adds a per-app handler for user-data administration.
// Typically called from an app's RegisterRoutes or init path once its DB is
// available.
func RegisterUserDataHandler(h UserDataHandler) {
	userDataMu.Lock()
	defer userDataMu.Unlock()
	userDataHandlers = append(userDataHandlers, h)
}

// RegisteredUserDataHandlers returns all registered user-data handlers,
// sorted by app name.
func RegisteredUserDataHandlers() []UserDataHandler {
	userDataMu.Lock()
	defer userDataMu.Unlock()
	out := make([]UserDataHandler, len(userDataHandlers))
	copy(out, userDataHandlers)
	sort.Slice(out, func(i, j int) bool { return out[i].AppName() < out[j].AppName() })
	return out
}

// UserDB returns a per-user sub-store for an app's private data. Apps call
// this from their authenticated request handlers to keep each user's data
// isolated.
//
// Returns nil when base is nil or uid is empty. Callers must authenticate
// the request (e.g. via AuthCurrentUser) and treat a nil result as
// unauthorized.
func UserDB(base Database, uid string) Database {
	if base == nil || uid == "" {
		return nil
	}
	return base.Sub("user:" + uid)
}

// RequireUser is a small helper for app HTTP handlers: it resolves the
// authenticated user and returns their per-app sub-store. If no user is
// authenticated it writes a 401 and returns ("", nil, false).
func RequireUser(w http.ResponseWriter, r *http.Request, base Database) (string, Database, bool) {
	uid := AuthCurrentUser(r)
	if uid == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return "", nil, false
	}
	return uid, UserDB(base, uid), true
}

// ForeignUsersLister returns the IDs of synthetic per-app users — the
// "user:phantom:<chatID>" / etc. namespaces that don't exist in the
// AuthTable but DO have UserDB sub-stores. Apps register a lister at
// startup so cross-app surfaces (e.g. Agency wanting to find phantom-
// dispatched sessions for an agent) can enumerate without depending
// on the originating app's package directly.
//
// The function returns canonical usernames you'd pass to UserDB —
// for phantom, that's "phantom:<chatID>". Empty / nil return when
// the app has no foreign users.
type ForeignUsersLister func() []string

var (
	foreignUsersMu      sync.Mutex
	foreignUsersListers []ForeignUsersLister
)

// RegisterForeignUsersLister adds an app's foreign-user enumerator.
// Idempotent calls are fine; duplicates are tolerated. Typically
// called once during app init.
func RegisterForeignUsersLister(fn ForeignUsersLister) {
	if fn == nil {
		return
	}
	foreignUsersMu.Lock()
	defer foreignUsersMu.Unlock()
	foreignUsersListers = append(foreignUsersListers, fn)
}

// ListForeignUsers returns every foreign-user ID from every registered
// lister. Used by Agency / admin surfaces that need to enumerate
// synthetic users across apps. Deduplicates so the same ID isn't
// returned twice if two apps happen to register the same name.
func ListForeignUsers() []string {
	foreignUsersMu.Lock()
	defer foreignUsersMu.Unlock()
	seen := map[string]bool{}
	var out []string
	for _, fn := range foreignUsersListers {
		for _, u := range fn() {
			if u == "" || seen[u] {
				continue
			}
			seen[u] = true
			out = append(out, u)
		}
	}
	return out
}

// dropUserScopes removes everything filed under uid: the "user:<uid>" scope
// in the root store and in every registered app's store. Per-user data is
// keyed by the username and nothing else, so an account deleted without this
// left its agents, apps, memory and records in place for whoever was next
// created with the same name. Dropping the scope root takes every table under
// it in one call and cannot reach a longer name ("user:bob2"). Returns how
// many stores it cleared.
//
// Runs after each app's own handler (UserDataHandler) has moved or purged
// what it owns, so what is left is only the person's own: their sessions,
// memory, records. An app with a private database file of its own clears it
// in its handler; this reaches the stores the framework handed out.
func dropUserScopes(uid string) int {
	if uid == "" {
		return 0
	}
	var done []Database
	n := 0
	drop := func(db Database) {
		if db == nil {
			return
		}
		for _, d := range done {
			if sameStore(d, db) {
				return
			}
		}
		done = append(done, db)
		db.Drop("user:" + uid)
		n++
	}
	drop(RootDB)
	for _, a := range append(append([]Agent(nil), RegisteredAgents()...), RegisteredApps()...) {
		if c := a.Get(); c != nil {
			drop(c.DB)
		}
	}
	return n
}

// sameStore reports two handles on one store. A handle of a type that cannot
// be compared is treated as distinct: dropping a scope twice is harmless.
func sameStore(a, b Database) (same bool) {
	defer func() {
		if recover() != nil {
			same = false
		}
	}()
	return a == b
}

// The core-held things a person builds: tools, skills and collections. Their
// handler is registered here because their storage is core's own.
type coreUserData struct{}

func init() { RegisterUserDataHandler(coreUserData{}) }

func (coreUserData) AppName() string { return "tools, skills and collections" }

func (coreUserData) Describe(uid string) UserDataSummary {
	sum := UserDataSummary{AppName: "tools, skills and collections", Counts: map[string]int{}, Actions: []string{"reassign", "purge"}}
	if uid == "" {
		return sum
	}
	for _, t := range LoadPersistentTempTools(nil, uid) {
		sum.Counts["tools"]++
		if t.Shared || len(t.SharedWith) > 0 {
			sum.Counts["shared"]++
		}
	}
	for _, sk := range LoadSkills(nil, uid) {
		sum.Counts["skills"]++
		if len(sk.AllowedUsers) > 0 {
			sum.Counts["shared"]++
		}
	}
	if pub := PublishedSkillsBy(nil, uid); len(pub) > 0 {
		sum.Counts["skills"] += len(pub)
		sum.Counts["shared"] += len(pub)
	}
	if udb := UserDB(CollectionsDB(), uid); udb != nil {
		for _, k := range udb.Keys(CollectionsTable) {
			var c Collection
			if udb.Get(CollectionsTable, k, &c) && c.Owner == uid {
				sum.Counts["collections"]++
				if len(c.AllowedUsers) > 0 {
					sum.Counts["shared"]++
				}
			}
		}
	}
	if RootDB != nil {
		for _, k := range RootDB.Keys(GlobalCollectionsTable) {
			var c Collection
			if RootDB.Get(GlobalCollectionsTable, k, &c) && c.Owner == uid {
				sum.Counts["collections"]++
				sum.Counts["shared"]++
			}
		}
	}
	return sum
}

// CheckReassign is the one refusal a move of these can hit: a tool name the
// new owner already uses.
func (coreUserData) CheckReassign(from, to string) error { return checkToolsMove(from, to) }

// Reassign moves tools first: they are the one kind a name clash can refuse,
// and refusing before anything else has moved leaves the account whole.
func (coreUserData) Reassign(from, to string) error {
	if err := moveToolsTo(from, to); err != nil {
		return err
	}
	moveSkillsTo(from, to)
	moveCollectionsTo(from, to)
	return nil
}

func (coreUserData) Anonymize(string) error { return ErrUserDataActionNotSupported }

func (coreUserData) Purge(uid string) error {
	purgeTools(uid)
	purgeSkills(uid)
	purgeCollections(uid)
	forgetRecipient(uid)
	removeUserImageFolders(uid)
	if AuthDB != nil {
		if n := promotion.DropOwnerRequests(AuthDB(), uid); n > 0 {
			Log("[userdata] dropped %d pending promotion request(s) from %s", n, uid)
		}
	}
	return nil
}

// removeUserImageFolders deletes a user's pictures from disk: the kept
// library, the recent rings and the delivered attachments, each a folder
// named for them under the image folder. They are the person's own, like
// their conversations, and went nowhere when the account did: a new account
// later given the same name would have found them waiting.
func removeUserImageFolders(uid string) {
	base := strings.TrimSpace(ImageDir())
	if base == "" || strings.TrimSpace(uid) == "" {
		return
	}
	name := safeRecentUser(uid)
	if name == "" || name == "." || name == ".." {
		return
	}
	for _, sub := range []string{"kept", "recent", "delivered"} {
		dir := filepath.Join(base, sub, name)
		if _, err := os.Stat(dir); err != nil {
			continue
		}
		if err := os.RemoveAll(dir); err != nil {
			Log("[userdata] could not remove %s's %s pictures: %v", uid, sub, err)
			continue
		}
		Log("[userdata] removed %s's %s pictures", uid, sub)
	}
}

// removedUserMarker stands in a list that would be left empty by a deleted
// account, where empty means everyone: it matches nobody (it cannot be a
// username), and it says to whoever reads the list why it is there.
func removedUserMarker(uid string) string { return "(removed account: " + uid + ")" }

// dropListedUser takes uid out of list. When emptyMeansEveryone and uid was
// the only name, the marker takes its place: removing the last allowed person
// must not open the thing to the whole deployment.
func dropListedUser(list []string, uid string, emptyMeansEveryone bool) ([]string, bool) {
	out := list[:0:0]
	found := false
	for _, u := range list {
		if u == uid {
			found = true
			continue
		}
		out = append(out, u)
	}
	if !found {
		return list, false
	}
	if len(out) == 0 && emptyMeansEveryone {
		out = []string{removedUserMarker(uid)}
	}
	return out, true
}

// forgetRecipient takes a deleted account off everything shared with it, the
// core-held kinds: skills, collections (readers and contributors), tools (the
// people it is shared with and its adopt list), credentials, and feature
// access. Shares are filed by username, so an entry left behind would reach
// whoever is next given the name. The share indexes go, and so do the names
// in the records, or the owner's next save would rebuild the index from them.
func forgetRecipient(uid string) {
	if RootDB == nil || uid == "" {
		return
	}
	for _, t := range []string{sharedSkillsTable, sharedCollectionsTable, sharedToolsTable} {
		peershare.DropRecipient(RootDB, t, uid)
	}
	for _, owner := range RootDB.Keys(skillsTable) {
		var rows []SkillRecord
		if !RootDB.Get(skillsTable, owner, &rows) {
			continue
		}
		changed := false
		for i := range rows {
			var c bool
			if rows[i].AllowedUsers, c = dropListedUser(rows[i].AllowedUsers, uid, false); c {
				changed = true
			}
		}
		if changed {
			RootDB.Set(skillsTable, owner, rows)
		}
	}
	forgetCollection := func(db Database, table, key string) {
		var c Collection
		if !db.Get(table, key, &c) {
			return
		}
		var a, b bool
		c.AllowedUsers, a = dropListedUser(c.AllowedUsers, uid, false)
		c.Contributors, b = dropListedUser(c.Contributors, uid, false)
		if a || b {
			db.Set(table, key, c)
		}
	}
	if AuthDB != nil {
		for _, u := range AuthListUsers(AuthDB()) {
			if udb := UserDB(CollectionsDB(), u.Username); udb != nil {
				for _, k := range udb.Keys(CollectionsTable) {
					forgetCollection(udb, CollectionsTable, k)
				}
			}
		}
	}
	for _, k := range RootDB.Keys(GlobalCollectionsTable) {
		forgetCollection(RootDB, GlobalCollectionsTable, k)
	}
	tempToolPersistMu.Lock()
	for _, owner := range RootDB.Keys(persistentTempToolsTable) {
		var list []PersistentTempTool
		if !RootDB.Get(persistentTempToolsTable, owner, &list) {
			continue
		}
		changed := false
		for i := range list {
			var a, b bool
			list[i].SharedWith, a = dropListedUser(list[i].SharedWith, uid, false)
			list[i].AllowedUsers, b = dropListedUser(list[i].AllowedUsers, uid, true)
			changed = changed || a || b
		}
		if changed {
			RootDB.Set(persistentTempToolsTable, owner, list)
		}
	}
	tempToolPersistMu.Unlock()
	Secure().forgetRecipient(uid)
	for _, f := range RootDB.Keys(featureAccessTable) {
		var p FeatureAccessPolicy
		if !RootDB.Get(featureAccessTable, f, &p) {
			continue
		}
		var c bool
		if p.AllowedUsers, c = dropListedUser(p.AllowedUsers, uid, true); c {
			RootDB.Set(featureAccessTable, f, p)
		}
	}
}
