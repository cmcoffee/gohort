package decide

import (
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/checkstreak"
	"github.com/cmcoffee/gohort/core/notices"
)

func init() { checkstreak.OnStreak = tellStreak }

// tellStreak tells each admin, on their bell, that check has given no verdict
// n times in a row on model, and why the last one gave none. A repeat folds
// into the same notice (core/notices), so a check failing all day is one row.
// With no RootDB, notices.Record keeps nothing.
func tellStreak(check, model, reason string, n int) {
	if AuthDB == nil {
		return
	}
	db := AuthDB()
	if db == nil {
		return
	}
	note := streakNotice(check, model, reason, n)
	for _, u := range AuthListUsers(db) {
		if !u.Admin {
			continue
		}
		note.Owner = u.Username
		notices.Record(RootDB, note)
	}
}

// streakNotice is the notice for check's run of n no-verdicts on model.
func streakNotice(check, model, reason string, n int) notices.Notice {
	name := checkLabel(check)
	if strings.TrimSpace(model) == "" {
		model = "its model"
	}
	return notices.Notice{
		Kind:  notices.KindStopped,
		Title: fmt.Sprintf("%s is giving no verdict on %s", name, model),
		Body: fmt.Sprintf("The last %d replies from %s to %s (%s) could not be used, so each time the check fell back to what it does without a verdict. The last reply was %s.\n\n"+
			"Admin > Models > Decision Offloading > Check tests shows each case's reply. Mapping the check to another model, or changing what it asks, ends this.",
			n, model, name, check, reason),
	}
}

// checkLabel is check's name as the admin pages show it, or its key.
func checkLabel(check string) string {
	for _, c := range JudgeCalls() {
		if c.Key == check && strings.TrimSpace(c.Label) != "" {
			return c.Label
		}
	}
	return check
}
