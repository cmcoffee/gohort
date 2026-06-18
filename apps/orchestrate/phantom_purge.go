package orchestrate

import (
	"context"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// One-time cleanup of leftover data from the retired phantom app (the app code
// is deleted; this purges what it left in the DB). Registered as an admin-
// triggered maintenance action (Admin → Maintenance) rather than an auto-
// migration because it's irreversible — the operator runs it when ready.
func init() {
	RegisterMaintenanceFunc(
		"purge_phantom_data",
		"Purge phantom data",
		"Delete all leftover data from the retired phantom app: its phantom_* tables "+
			"(convos, messages, outbox, config, …), the per-chat \"phantom:\" user scopes "+
			"and every session under them, and orphaned phantom_chat triggers / "+
			"phantom.callback scheduled tasks. Irreversible.",
		purgePhantomData,
	)
}

func purgePhantomData(ctx context.Context) int {
	if RootDB == nil {
		return 0
	}
	count := 0

	// 1. Orphaned phantom_* top-level tables + per-chat "phantom:" user scopes.
	// AllTables() returns raw bucket names INCLUDING the \x1f-separated sub-store
	// namespaces Tables() hides. Drop() cascades to a bucket's sub-buckets, so
	// dropping the "user:phantom:<chatID>" scope root removes every table under
	// it in one call — dedupe roots so we Drop each once.
	dropped := map[string]bool{}
	for _, t := range RootDB.AllTables() {
		var target string
		switch {
		case strings.HasPrefix(t, "phantom_"):
			target = t // a top-level phantom table (phantom_convos, phantom_outbox, …)
		case strings.HasPrefix(t, "user:phantom:"):
			// Reduce to the scope root "user:phantom:<chatID>" (cut the \x1f tail);
			// Drop() then removes every table inside that user scope.
			target = t
			if i := strings.IndexRune(t, '\x1f'); i >= 0 {
				target = t[:i]
			}
		default:
			continue
		}
		if target == "" || dropped[target] {
			continue
		}
		dropped[target] = true
		RootDB.Drop(target)
		count++
	}

	// 2. Orphaned phantom_chat scheduled triggers (per owner — they were created
	// by real users targeting a phantom chat; no handler fires them now).
	for _, u := range AuthListUsers(RootDB) {
		for _, tr := range ListScheduledTriggers(RootDB, u.Username) {
			if tr.TargetKind == "phantom_chat" {
				DeleteScheduledTrigger(RootDB, u.Username, tr.Name)
				count++
			}
		}
	}

	// 3. Orphaned phantom.callback scheduled tasks (global, keyed by kind).
	for _, task := range ListScheduledTasks("phantom.callback") {
		UnscheduleTask(task.ID)
		count++
	}

	Log("[maintenance] purge_phantom_data: removed %d phantom tables/scopes/schedules", count)
	return count
}
