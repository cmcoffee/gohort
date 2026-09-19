package orchestrate

// The goals view: every objective in flight, over three records that each keep
// theirs in a different shape.

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

// A schedule without an Until is a cadence, not a goal. The page exists because
// those are the overwhelming majority, and scanning past them is the work it
// removes.
func TestOnlyASchedulingRecordWithAGoalAppears(t *testing.T) {
	src := readFile(t, "console_goals.go")
	// Each of the three loops skips an empty Until before doing anything else.
	if n := strings.Count(src, `strings.TrimSpace(`); n < 3 {
		t.Error("fewer than three Until checks; one of the surfaces lists its cadences as goals")
	}
	for _, surface := range []string{"ListStandingAgents", "listAgentRecurringTasks", "ListEventMonitors"} {
		if !strings.Contains(src, surface) {
			t.Errorf("the union does not read %s, so that surface's goals are invisible here", surface)
		}
	}
}

// The three states are exclusive, which is what makes them sections rather than
// overlapping filters. Stalled wins over met: a park is the thing to read
// first, and a parked objective's last attempt was by definition not met.
func TestAGoalIsFiledUnderExactlyOneSection(t *testing.T) {
	cases := []struct {
		name          string
		broken, met   bool
		wantSection   string
		wantFlag      string
		otherFlagsOff []string
	}{
		{"still working", false, false, goalSectionInFlight, "_in_flight", []string{"_met", "_stalled"}},
		{"met", false, true, goalSectionMet, "_met", []string{"_in_flight", "_stalled"}},
		{"stalled", true, false, goalSectionStalled, "_stalled", []string{"_met", "_in_flight"}},
		{"stalled after an unmet run", true, true, goalSectionStalled, "_stalled", []string{"_met", "_in_flight"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			row := goalRow(consoleGoalRow{Goal: "the PR is merged", ID: "x"}, c.broken, c.met)
			if row == nil {
				t.Fatal("the row would not convert")
			}
			if row["_section"] != c.wantSection {
				t.Errorf("section = %v, want %q", row["_section"], c.wantSection)
			}
			if row[c.wantFlag] != true {
				t.Errorf("%s is not set, so its chip will not find it", c.wantFlag)
			}
			for _, off := range c.otherFlagsOff {
				if row[off] == true {
					t.Errorf("%s is also set: one goal would be counted under two chips", off)
				}
			}
		})
	}
}

// Every chip has to name a flag a row actually carries, or it filters to
// nothing and reads as a page with no goals on it.
func TestEveryGoalChipNamesAFlagTheRowsCarry(t *testing.T) {
	rows := []map[string]any{
		goalRow(consoleGoalRow{Goal: "a", ID: "a"}, false, false),
		goalRow(consoleGoalRow{Goal: "b", ID: "b"}, false, true),
		goalRow(consoleGoalRow{Goal: "c", ID: "c"}, true, false),
	}
	for _, f := range goalsFilters() {
		if first := f.Options[0]; first.Field != "" {
			t.Errorf("filter %q opens on %q, which hides rows before anybody chose to", f.Label, first.Label)
		}
		for _, opt := range f.Options[1:] {
			found := false
			for _, row := range rows {
				if row[opt.Field] == true {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("chip %q reads %q, which no goal row sets", opt.Label, opt.Field)
			}
		}
	}
}

// An unbounded goal must not be printed as a bounded one: "3 attempts" and
// "3 of 5" are different facts, and the second tells the owner their goal will
// stop when it will not.
func TestAnUnboundedGoalShowsNoDenominator(t *testing.T) {
	cases := []struct {
		used, max int
		want      string
	}{
		{0, 5, "0 of 5 attempts"},
		{3, 5, "3 of 5 attempts"},
		{1, 0, "1 attempt"},
		{3, 0, "3 attempts"},
		{0, 0, "0 attempts"},
		// objectiveAttemptNumber-1 can go negative on a restored payload whose
		// base ran ahead of its count.
		{-2, 0, "0 attempts"},
	}
	for _, c := range cases {
		if got := goalAttemptLabel(c.used, c.max); got != c.want {
			t.Errorf("goalAttemptLabel(%d, %d) = %q, want %q", c.used, c.max, got, c.want)
		}
	}
}

// The last judged attempt decides, and the surfaces all append newest-last.
func TestMetReadsTheLastAttemptNotAnyAttempt(t *testing.T) {
	if objectiveMet(nil) {
		t.Error("a goal with no attempts reads as met")
	}
	if !objectiveMet([]ObjectiveAttempt{{Met: false}, {Met: true}}) {
		t.Error("a goal met on its latest attempt does not read as met")
	}
	if objectiveMet([]ObjectiveAttempt{{Met: true}, {Met: false}}) {
		t.Error("a goal met earlier and unmet since reads as met, so it leaves the page while still outstanding")
	}
}

// Two pages compose the same records and want opposite orders: the Scheduler
// leads with what runs next, this leads with what is stuck. A shared rank table
// would make one page's sections all tie, and tied sections interleave — which
// draws every heading again and again down the page.
func TestEachPageChoosesItsOwnSectionOrder(t *testing.T) {
	rows := []map[string]any{
		{"_section": goalSectionMet, "name": "c"},
		{"_section": goalSectionInFlight, "name": "b"},
		{"_section": goalSectionStalled, "name": "a"},
	}
	ui.SortRowsBySection(rows, []string{goalSectionStalled, goalSectionInFlight, goalSectionMet})
	var got []string
	for _, r := range rows {
		got = append(got, r["_section"].(string))
	}
	want := strings.Join([]string{goalSectionStalled, goalSectionInFlight, goalSectionMet}, ",")
	if strings.Join(got, ",") != want {
		t.Errorf("order = %v, want %v", got, want)
	}

	// And each section is still drawn once.
	seen, last := map[string]int{}, ""
	for _, r := range rows {
		if s := r["_section"].(string); s != last {
			seen[s]++
			last = s
		}
	}
	for section, runs := range seen {
		if runs != 1 {
			t.Errorf("%s is drawn %d times", section, runs)
		}
	}
}

// Read-only about the SCHEDULE, on purpose: every row here is editable from the
// Scheduler, and a second surface with its own copies of those buttons is a
// second set of rules for the same record.
//
// One exception, and the shape of it is the rule. A task's notes are what its
// runs have worked out so far, which is the question this page asks one row
// down rather than a verb that changes anything; and it is the SAME client
// action the Scheduler mounts, one endpoint and one write path, not a copy. So
// the test stopped counting actions and started reading them: no creator, no
// method that writes to a schedule, nothing but that one door.
func TestTheGoalsViewOwnsNoVerbs(t *testing.T) {
	page := readFile(t, "page_chat.go")
	found := 0
	for _, entry := range navEntries(t, page) {
		if entry.label != "Goals" {
			continue
		}
		found++
		// A list-level button on a read-only page would be a creator, which is
		// the Scheduler's job on the page that lists what it creates.
		if strings.Contains(entry.body, "ViewActions") {
			t.Errorf("the %s Goals entry declares a list-level action; creating a schedule belongs on the page that lists them", entry.menu)
		}
		for _, line := range strings.Split(entry.body, "\n") {
			if !strings.Contains(line, "{Label:") || !strings.Contains(line, "URL:") {
				continue
			}
			if !strings.Contains(line, `"orchestrate_task_notes"`) {
				t.Errorf("the %s Goals entry declares an action other than Notes; the page that owns the record owns the verbs:\n  %s",
					entry.menu, strings.TrimSpace(line))
			}
			if !strings.Contains(line, `Method: "client"`) {
				t.Errorf("the %s Goals entry declares a server method; this page reads:\n  %s", entry.menu, strings.TrimSpace(line))
			}
		}
		if !strings.Contains(entry.body, "goalsFilters()") {
			t.Errorf("the %s Goals entry has no filters", entry.menu)
		}
	}
	// A loop over nothing passes every assertion in it.
	if found != 2 {
		t.Fatalf("found %d Goals entries, want one per menu (Manage and Fleet)", found)
	}
}

// The boundary the two halves of the row builder now express in their names.
//
// A goal row is SHAPING plus this page's own fields. It must not pick up the
// Scheduler's action flags (they would light up buttons this page does not
// declare) or its filter fields (nothing here reads them, and a row carrying
// answers to questions nobody asks is how the next reader learns the wrong
// rule). It carries its own _kind, which is the field the empty-kind sentinel
// used to overwrite.
func TestAGoalRowCarriesNoSchedulerFlags(t *testing.T) {
	m := goalRow(consoleGoalRow{
		Goal: "the backlog is empty", Where: "Scheduled agent · nightly",
		ID: "nightly", Kind: schedKindStanding, Notes: true,
	}, false, false)
	if m == nil {
		t.Fatal("no row")
	}
	for _, gone := range []string{
		"_run_standing", "_pause_standing", "_del_standing", "_edit_recurring",
		"_test_monitor", "_attention", "_at_rest",
	} {
		if _, present := m[gone]; present {
			t.Errorf("a goal row carries %s, which belongs to the page that owns the verbs", gone)
		}
	}
	if m["_kind"] != schedKindStanding {
		t.Errorf("a goal row lost the kind its notes action resolves by: %v", m["_kind"])
	}
	if m["_section"] != goalSectionInFlight {
		t.Errorf("a goal row is not filed under a section: %v", m["_section"])
	}
}
