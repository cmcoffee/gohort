package orchestrate

import (
	"context"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/timing"
)

// Once a month, the way people say it, reads as a monthly timing. An agent
// asked for "near the end of the month" tried "monthly last day 9:00",
// "27th 09:00" and "monthly 28th 09:00", was refused each time, and told the
// user the scheduler could not do it at all.
func TestTheScheduleToolReadsAMonthlyDay(t *testing.T) {
	for in, want := range map[string]string{
		"monthly 28th 09:00":                       "MONTHLY-28 09:00",
		"27th 09:00":                               "MONTHLY-27 09:00",
		"monthly last day 9:00":                    "MONTHLY-LAST 09:00",
		"monthly on the 28th at 9am":               "MONTHLY-28 09:00",
		"every month on the 1st at 8:30pm":         "MONTHLY-1 20:30",
		"each month on the last day at 9am":        "MONTHLY-LAST 09:00",
		"the last day of the month at 9am":         "MONTHLY-LAST 09:00",
		"28th of every month at 9am":               "MONTHLY-28 09:00",
		"the 2nd to last day of each month at 9am": "MONTHLY-LAST2 09:00",
		"monthly on the 3rd-to-last day at 18:00":  "MONTHLY-LAST3 18:00",
		"monthly 15 09:00":                         "MONTHLY-15 09:00",
		"MONTHLY-LAST 09:00":                       "MONTHLY-LAST 09:00",
	} {
		cron, interval, err := parseEverySpec(in)
		if err != nil || interval != 0 || cron != want {
			t.Errorf("%q: %q %d %v, want %q", in, cron, interval, err, want)
			continue
		}
		spec, ok, err := everyTiming("u", map[string]any{}, cron, 0, nil)
		if !ok || err != nil || spec.How != timing.HowMonthly {
			t.Errorf("%q: timing %+v %v %v, want monthly", in, spec, ok, err)
		}
	}
	for _, bad := range []string{"monthly 32nd 09:00", "monthly on the 0th at 9am", "monthly on the 9th to last day at 9am"} {
		if cron, _, err := parseEverySpec(bad); err == nil {
			t.Errorf("%q was accepted as %q", bad, cron)
		}
	}
	// A number alone is not a day of the month, and the forms already read
	// still read as before.
	for in, want := range map[string]string{
		"daily 08:00":                 "daily 08:00",
		"FRI 21:30":                   "FRI 21:30",
		"every year on Dec 25 at 9am": "DEC-25 09:00",
	} {
		if cron, _, err := parseEverySpec(in); err != nil || cron != want {
			t.Errorf("%q now reads as %q %v", in, cron, err)
		}
	}
	if cron, _, err := parseEverySpec("28 09:00"); err == nil && cron != "" && cron[:7] == "MONTHLY" {
		t.Errorf("a bare number read as monthly: %q", cron)
	}
}

// Through the whole tool: a fleet schedule fires on its day and says so, and
// an agent's own recurring task carries the monthly timing (the pattern
// fields are only its time of day).
func TestAMonthlyScheduleIsMade(t *testing.T) {
	db := pinRootDB(t)
	PreInitScheduler()
	tool := scheduleTool(t, &ToolSession{Username: "craig"}, "agent-1")
	if _, err := tool.Handler(context.Background(), map[string]any{
		"when": "every", "time": "monthly on the last day at 9am", "then": "post next month's big PC releases", "name": "release-roundup",
	}); err != nil {
		t.Fatal(err)
	}
	sa, ok := GetStandingAgent(db, "craig", "release-roundup")
	if !ok || StandingScheduleLabel(sa) != "every month on the last day at 09:00" {
		t.Fatalf("monthly schedule wrong: %+v (%q)", sa, StandingScheduleLabel(sa))
	}
	from := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	eff, _ := sa.EffectiveTiming()
	if next, err := eff.Next(from, nil); err != nil || !next.Equal(time.Date(2026, 11, 30, 9, 0, 0, 0, time.UTC)) {
		t.Errorf("next run %v %v, want Nov 30 09:00", next, err)
	}
	// Its stored cron reads the same way where only the cron is looked at.
	if sa.Cron != "" {
		if next, err := NextCronOccurrence(sa.Cron, from); err != nil || !next.Equal(time.Date(2026, 11, 30, 9, 0, 0, 0, time.UTC)) {
			t.Errorf("cron %q next %v %v", sa.Cron, next, err)
		}
	}

	rec := &recordingRecurring{}
	own := scheduleToolDef(nil, "craig", "a1", nil, rec)
	if _, err := own.Handler(context.Background(), map[string]any{
		"when": "every", "time": "monthly 28th 09:00", "then": "post the roundup", "name": "roundup",
	}); err != nil {
		t.Fatal(err)
	}
	if len(rec.scheduled) != 1 {
		t.Fatalf("the recurring path was not used: %v", rec.scheduled)
	}
	got := rec.scheduled[0]
	spec, ok, err := timingArg(got)
	if !ok || err != nil || spec.How != timing.HowMonthly || spec.Day != 28 || spec.Times[0] != 540 {
		t.Errorf("the recurring task lost its monthly timing: %v (%+v %v)", got, spec, err)
	}
	if got["pattern"] != RecurringDaily || got["daily_at"] != "09:00" {
		t.Errorf("pattern fields: %v", got)
	}
}
