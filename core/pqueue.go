package core

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"sync"
)

const pqueueTable = "persistent_queue"

// QueueEntry represents a persistent queue item that survives server restarts.
type QueueEntry struct {
	ID          string          `json:"id"`
	App         string          `json:"app"`                    // app identifier
	Label       string          `json:"label"`                  // human-readable label (topic, question)
	Params      json.RawMessage `json:"params"`                 // app-specific parameters
	NotifyUser  string          `json:"notify_user,omitempty"`  // backwards compat: single user (migrated to NotifyUsers)
	NotifyUsers []string        `json:"notify_users,omitempty"` // users to notify on completion
	Order       int             `json:"order"`                  // FIFO ordering
}

// QueueHandler is called for each restored queue entry on startup.
// The handler is responsible for re-running the task.
type QueueHandler func(entry QueueEntry)

var (
	qhandlerMu  sync.Mutex
	qhandlers   = make(map[string]QueueHandler)
	queueDBFunc func() Database
)

// RegisterQueueHandler registers a restore handler for a given app name.
// Called during init() by apps that support persistent queuing.
func RegisterQueueHandler(app string, handler QueueHandler) {
	qhandlerMu.Lock()
	defer qhandlerMu.Unlock()
	qhandlers[app] = handler
}

// SetQueueDB sets the database used by the persistent queue.
// Called by the application at startup.
func SetQueueDB(dbFunc func() Database) {
	queueDBFunc = dbFunc
}

func queueDB() Database {
	if queueDBFunc != nil {
		return queueDBFunc()
	}
	return nil
}

// QueueAdd adds an item to the persistent queue. Params should be a
// JSON-marshalable value with app-specific data.
func QueueAdd(id, app, label string, params interface{}, notify_user string) {
	db := queueDB()
	if db == nil {
		return
	}

	var raw json.RawMessage
	if params != nil {
		raw, _ = json.Marshal(params)
	}

	// Find the next order number.
	max_order := 0
	for _, key := range db.Keys(pqueueTable) {
		var entry QueueEntry
		if db.Get(pqueueTable, key, &entry) && entry.Order > max_order {
			max_order = entry.Order
		}
	}

	var users []string
	if notify_user != "" {
		users = []string{notify_user}
	}
	entry := QueueEntry{
		ID:          id,
		App:         app,
		Label:       label,
		Params:      raw,
		NotifyUsers: users,
		Order:       max_order + 1,
	}
	db.Set(pqueueTable, id, entry)
	Debug("[queue] persisted %s/%s at position %d", app, id[:8], entry.Order)
}

// QueueRemove removes a completed item from the persistent queue.
func QueueRemove(id string) {
	db := queueDB()
	if db == nil {
		return
	}
	db.Unset(pqueueTable, id)
	Debug("[queue] removed %s", id[:8])
}

// QueueGetNotifyUsers returns the current notification users for a queue item.
// Reads from the database so it reflects mid-run toggles.
// Handles backwards compat with the old single NotifyUser field.
func QueueGetNotifyUsers(id string) []string {
	db := queueDB()
	if db == nil {
		return nil
	}
	var entry QueueEntry
	if !db.Get(pqueueTable, id, &entry) {
		return nil
	}
	// Backwards compat: migrate old single field.
	if len(entry.NotifyUsers) == 0 && entry.NotifyUser != "" {
		return []string{entry.NotifyUser}
	}
	return entry.NotifyUsers
}

// QueueHasNotifyUser checks if a specific user is in the notify list.
func QueueHasNotifyUser(id, username string) bool {
	for _, u := range QueueGetNotifyUsers(id) {
		if u == username {
			return true
		}
	}
	return false
}

// HandleQueueNotify returns an http.HandlerFunc that toggles notification
// for a queued/running item. Multiple users can subscribe independently.
func HandleQueueNotify() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		if id == "" {
			http.Error(w, "id required", http.StatusBadRequest)
			return
		}
		username := AuthCurrentUser(r)
		if username == "" {
			http.Error(w, "not authenticated", http.StatusUnauthorized)
			return
		}

		db := queueDB()
		if db == nil {
			http.Error(w, "no database", http.StatusInternalServerError)
			return
		}

		var entry QueueEntry
		if !db.Get(pqueueTable, id, &entry) {
			// Item may have already finished -- just acknowledge.
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{"notify": true})
			return
		}

		// Backwards compat: migrate old single field into list.
		if len(entry.NotifyUsers) == 0 && entry.NotifyUser != "" {
			entry.NotifyUsers = []string{entry.NotifyUser}
			entry.NotifyUser = ""
		}

		// Toggle: add or remove this user from the list.
		found := false
		var updated []string
		for _, u := range entry.NotifyUsers {
			if u == username {
				found = true
				continue // remove
			}
			updated = append(updated, u)
		}
		if !found {
			updated = append(updated, username)
		}
		entry.NotifyUsers = updated
		entry.NotifyUser = "" // clear legacy field
		db.Set(pqueueTable, id, entry)

		enabled := !found
		// Persist the user's preference so future jobs auto-subscribe.
		if AuthDB != nil {
			AuthSetNotifyDefault(AuthDB(), username, enabled)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"notify": enabled})
	}
}

// HandleQueueNotifyStatus returns an http.HandlerFunc that reports whether
// the current user is subscribed to notifications for a given item.
func HandleQueueNotifyStatus() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		username := AuthCurrentUser(r)
		subscribed := false
		if id != "" && username != "" {
			subscribed = QueueHasNotifyUser(id, username)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"notify": subscribed})
	}
}

// QueueRestore loads all persisted queue entries, sorts them by order,
// and dispatches each to the registered handler for its app. Entries
// with no registered handler are skipped (logged as warnings).
func QueueRestore() {
	db := queueDB()
	if db == nil {
		return
	}

	var entries []QueueEntry
	for _, key := range db.Keys(pqueueTable) {
		var entry QueueEntry
		if db.Get(pqueueTable, key, &entry) {
			entries = append(entries, entry)
		}
	}

	if len(entries) == 0 {
		return
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].Order < entries[j].Order })
	Log("[queue] restoring %d queued items on startup", len(entries))

	qhandlerMu.Lock()
	handlers := make(map[string]QueueHandler)
	for k, v := range qhandlers {
		handlers[k] = v
	}
	qhandlerMu.Unlock()

	for _, entry := range entries {
		handler, ok := handlers[entry.App]
		if !ok {
			Log("[queue] warning: no handler for app %q, skipping %s", entry.App, entry.ID[:8])
			continue
		}
		Log("[queue] restoring %s/%s: %s", entry.App, entry.ID[:8], truncLabel(entry.Label))
		go handler(entry)
	}
}

// QueueEntries returns all current persistent queue entries sorted by order.
func QueueEntries() []QueueEntry {
	db := queueDB()
	if db == nil {
		return nil
	}
	var entries []QueueEntry
	for _, key := range db.Keys(pqueueTable) {
		var entry QueueEntry
		if db.Get(pqueueTable, key, &entry) {
			entries = append(entries, entry)
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Order < entries[j].Order })
	return entries
}

// UnmarshalQueueParams decodes the app-specific params from a queue entry.
func UnmarshalQueueParams(entry QueueEntry, target interface{}) error {
	if entry.Params == nil {
		return fmt.Errorf("no params")
	}
	return json.Unmarshal(entry.Params, target)
}

func truncLabel(s string) string {
	if len(s) > 80 {
		return s[:80] + "..."
	}
	return s
}
