package servitor

import (
	"context"
	"fmt"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

const watchTable = "ssh_watches"

// ScheduledWatch is a condition the worker registered to poll every minute.
// When the command output contains Pattern, the watch triggers and is marked done.
// Modelled after expect(1): you start something, register a pattern to wait for,
// and the system tells you when it appears.
type ScheduledWatch struct {
	ID          string `json:"id"`
	ApplianceID string `json:"appliance_id"`
	UserID      string `json:"user_id"`
	Task        string `json:"task"`       // human description
	Command     string `json:"command"`    // SSH command to run each tick
	Pattern     string `json:"pattern"`    // substring in output → condition met
	TimeoutAt   string `json:"timeout_at"` // RFC3339 — give up after this
	NextRunAt   string `json:"next_run_at"` // RFC3339 — when to run next
	Created     string `json:"created"`
	Done        bool   `json:"done"`
}

// storeWatch persists a watch in the global app DB (not per-user) so the
// background runner can find it without iterating per-user namespaces.
func storeWatch(db Database, w ScheduledWatch) {
	if db == nil {
		return
	}
	db.Set(watchTable, w.ID, w)
}

// listWatches returns all non-done watches stored in the global DB.
func listWatches(db Database) []ScheduledWatch {
	if db == nil {
		return nil
	}
	var out []ScheduledWatch
	for _, k := range db.Keys(watchTable) {
		var w ScheduledWatch
		if db.Get(watchTable, k, &w) && !w.Done {
			out = append(out, w)
		}
	}
	return out
}

// listWatchesForAppliance returns active watches for a specific appliance.
func listWatchesForAppliance(db Database, applianceID string) []ScheduledWatch {
	var out []ScheduledWatch
	for _, w := range listWatches(db) {
		if w.ApplianceID == applianceID {
			out = append(out, w)
		}
	}
	return out
}

// execWatch runs the watch command via SSH and returns the output.
func execWatch(appliance Appliance, userID, command string) (string, error) {
	conn, err := acquireConn(userID, appliance)
	if err != nil {
		return "", fmt.Errorf("ssh connect: %w", err)
	}
	session, err := conn.NewSession()
	if err != nil {
		return "", fmt.Errorf("new session: %w", err)
	}
	defer session.Close()
	out, _ := session.CombinedOutput(command)
	result := strings.TrimSpace(string(out))
	if len(result) > 2000 {
		result = result[:2000]
	}
	return result, nil
}

// runWatchLoop runs the scheduled watch background ticker. Call once from RegisterRoutes.
func (T *Servitor) runWatchLoop(ctx context.Context) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			T.processWatches()
		}
	}
}

// processWatches checks all active watches and fires any that are due.
func (T *Servitor) processWatches() {
	now := time.Now()
	for _, w := range listWatches(T.DB) {
		// Skip if not yet due.
		if w.NextRunAt != "" {
			if next, err := time.Parse(time.RFC3339, w.NextRunAt); err == nil && now.Before(next) {
				continue
			}
		}
		// Check timeout.
		if w.TimeoutAt != "" {
			if deadline, err := time.Parse(time.RFC3339, w.TimeoutAt); err == nil && now.After(deadline) {
				w.Done = true
				T.DB.Set(watchTable, w.ID, w)
				udb := UserDB(T.DB, w.UserID)
				var appliance Appliance
				if udb.Get(applianceTable, w.ApplianceID, &appliance) {
					T.recordWatchResult(udb, w, appliance, "", true)
				}
				continue
			}
		}
		// Run the check in a separate goroutine so slow SSH doesn't block the loop.
		go T.checkWatch(w)
		// Advance next run regardless of check outcome.
		w.NextRunAt = now.Add(60 * time.Second).Format(time.RFC3339)
		T.DB.Set(watchTable, w.ID, w)
	}
}

// checkWatch executes one watch tick and triggers if the pattern is found.
func (T *Servitor) checkWatch(w ScheduledWatch) {
	udb := UserDB(T.DB, w.UserID)
	var appliance Appliance
	if !udb.Get(applianceTable, w.ApplianceID, &appliance) {
		return
	}
	output, err := execWatch(appliance, w.UserID, w.Command)
	if err != nil {
		return
	}
	if strings.Contains(output, w.Pattern) {
		w.Done = true
		T.DB.Set(watchTable, w.ID, w)
		T.recordWatchResult(udb, w, appliance, output, false)
	}
}

// recordWatchResult stores a fact and note when a watch completes or expires.
func (T *Servitor) recordWatchResult(udb Database, w ScheduledWatch, appliance Appliance, output string, expired bool) {
	var summary string
	if expired {
		summary = fmt.Sprintf("[Watch expired] %s — condition '%s' never matched. Command: %s", w.Task, w.Pattern, w.Command)
	} else {
		short := output
		if len(short) > 400 {
			short = short[:400] + "..."
		}
		summary = fmt.Sprintf("[Watch triggered] %s\nCommand: %s\nOutput: %s", w.Task, w.Command, short)
	}

	// Store as a fact so the lead/worker see it on next session.
	key := "watch_result:" + w.ID[:8]
	tag := "watch"
	if expired {
		tag = "watch_expired"
	}
	storeFact(udb, w.ApplianceID, appliance.Name, key, summary, "short", []string{tag})

	// Append to appliance notes so the lead's context includes it.
	var existing string
	udb.Get(notesTable, w.ApplianceID, &existing)
	entry := "- " + summary
	if existing != "" {
		udb.Set(notesTable, w.ApplianceID, existing+"\n"+entry)
	} else {
		udb.Set(notesTable, w.ApplianceID, entry)
	}
}
