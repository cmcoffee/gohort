package core

import (
	"errors"
	"net/http"
	"sort"
	"sync"
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
