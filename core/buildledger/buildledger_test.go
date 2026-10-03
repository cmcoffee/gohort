package buildledger

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

// memStore is a JSON round-tripping map, the way the real store encodes.
type memStore map[string]map[string][]byte

func (m memStore) Get(table, key string, out interface{}) bool {
	b, ok := m[table][key]
	if !ok {
		return false
	}
	return json.Unmarshal(b, out) == nil
}
func (m memStore) Set(table, key string, v interface{}) {
	if m[table] == nil {
		m[table] = map[string][]byte{}
	}
	b, _ := json.Marshal(v)
	m[table][key] = b
}
func (m memStore) Keys(table string) []string {
	var out []string
	for k := range m[table] {
		out = append(out, k)
	}
	return out
}

// fresh gives each test its own store, clock and stamp slots.
func fresh(t *testing.T) func(d time.Duration) {
	t.Helper()
	SetStore(memStore{})
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	clock := base
	now = func() time.Time { return clock }
	mu.Lock()
	pending = map[string]*pendingSet{}
	mu.Unlock()
	t.Cleanup(func() {
		SetStore(nil)
		now = time.Now
	})
	return func(d time.Duration) { clock = clock.Add(d) }
}

func rec(target string, v Verdict, classes ...string) Outcome {
	return Outcome{Owner: "u1", Session: "s1", Kind: KindTool, Target: target, Via: "tool_def test", Verdict: v, Classes: classes}
}

// Attempts to green is read per episode: the passing run closes one, and the
// next failure starts another. A tool green first time counts as a first-try
// pass; one still failing is open, not green.
func TestEpisodesSplitAtEachPass(t *testing.T) {
	tick := fresh(t)
	for _, o := range []Outcome{
		rec("weather", Fail, "pipe-compile"),
		rec("weather", Fail, "pipe-shape"),
		rec("weather", Pass),
		rec("weather", Fail, "live-status"), // edited later, broke again
		rec("weather", Pass),
		rec("notes", Pass),
		rec("cal", Unproven, "write-unfired"),
		rec("cal", Fail, "unsent-param"),
	} {
		tick(time.Minute)
		Record(o)
	}
	rep := Read(time.Time{})
	if rep.Verifications != 8 || rep.Passes != 3 || rep.Fails != 4 || rep.Unproven != 1 {
		t.Fatalf("counts = %d verifications, %d pass, %d fail, %d unproven", rep.Verifications, rep.Passes, rep.Fails, rep.Unproven)
	}
	if rep.Episodes != 4 || rep.Green != 3 || rep.Open != 1 || rep.FirstTry != 1 {
		t.Fatalf("episodes = %d, green %d, open %d, first try %d; want 4, 3, 1, 1", rep.Episodes, rep.Green, rep.Open, rep.FirstTry)
	}
	// Green episodes took 3, 2 and 1 attempts.
	if rep.MeanToGreen != 2 || rep.MedianToGreen != 2 {
		t.Fatalf("to green: mean %v, median %d; want 2, 2", rep.MeanToGreen, rep.MedianToGreen)
	}
	var open Episode
	for _, ep := range rep.Recent {
		if !ep.Green {
			open = ep
		}
	}
	if open.Target != "cal" || open.Attempts != 2 || !reflect.DeepEqual(open.Classes, []string{"unsent-param", "write-unfired"}) {
		t.Fatalf("open episode = %+v", open)
	}
}

// A class that hit many targets ranks above one target failing the same way
// many times: the first is a pattern in how things get built, the second is
// one stuck build.
func TestClassesRankByTargetsReached(t *testing.T) {
	tick := fresh(t)
	for i := 0; i < 4; i++ {
		tick(time.Minute)
		Record(rec("stuck", Fail, "pipe-shape"))
	}
	for _, target := range []string{"a", "b", "c"} {
		tick(time.Minute)
		Record(rec(target, Fail, "unsent-param", "unsent-param"))
	}
	rep := Read(time.Time{})
	if len(rep.Classes) != 2 || rep.Classes[0].Class != "unsent-param" || rep.Classes[0].Targets != 3 || rep.Classes[0].Count != 3 {
		t.Fatalf("classes = %+v", rep.Classes)
	}
	if rep.Classes[1].Class != "pipe-shape" || rep.Classes[1].Count != 4 || rep.Classes[1].Targets != 1 {
		t.Fatalf("second class = %+v", rep.Classes[1])
	}
}

// The model is not known where the verdict is, so Stamp fills it from the
// round's step callback. Only the stamped session's unstamped verdicts take
// it: another session's are not this round's, and one already stamped keeps
// the model that actually served it.
func TestStampFillsOnlyThatSessionsWaitingVerdicts(t *testing.T) {
	tick := fresh(t)
	Record(rec("weather", Fail, "pipe-compile"))
	Stamp("s1", "qwen", "worker", []string{"build_plan"})
	tick(time.Minute)
	Record(rec("weather", Pass))
	other := rec("weather", Fail, "live-status")
	other.Session = "s2"
	Record(other)
	Stamp("s1", "gemini", "lead", []string{"build_plan", "verify_first"})

	mu.Lock()
	var r row
	store.Get(table, rowKey(KindTool, "u1", "weather"), &r)
	mu.Unlock()
	got := []string{}
	for _, o := range r.Outcomes {
		got = append(got, o.Session+":"+o.Model+":"+o.Tier)
	}
	want := []string{"s1:qwen:worker", "s1:gemini:lead", "s2::"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("stamped = %v, want %v", got, want)
	}
	if !reflect.DeepEqual(r.Outcomes[1].Clauses, []string{"build_plan", "verify_first"}) {
		t.Fatalf("clauses = %v", r.Outcomes[1].Clauses)
	}
}

// A verdict nobody stamps in time gives up its slot rather than taking the
// model of whatever turn on that session stamps next.
func TestUnclaimedStampSlotExpires(t *testing.T) {
	tick := fresh(t)
	Record(rec("weather", Fail, "pipe-compile"))
	tick(pendingTTL + time.Minute)
	Record(rec("other", Pass)) // any later record prunes
	tick(time.Minute)
	other := rec("third", Pass)
	other.Session = "s9"
	Record(other)
	Stamp("s1", "gemini", "lead", nil)

	rep := Read(time.Time{})
	for _, ts := range rep.Tiers {
		if ts.Tier == "lead" && ts.Verifications != 1 {
			t.Fatalf("lead got %d verifications; only the run recorded after the expiry may be stamped", ts.Verifications)
		}
	}
}

// A target's history is bounded, the newest kept.
func TestHistoryIsBounded(t *testing.T) {
	tick := fresh(t)
	for i := 0; i < maxOutcomes+5; i++ {
		tick(time.Minute)
		Record(rec("busy", Fail, "live-status"))
	}
	rep := Read(time.Time{})
	if rep.Verifications != maxOutcomes {
		t.Fatalf("kept %d, want %d", rep.Verifications, maxOutcomes)
	}
}

// The window counts the runs inside it, and an episode by when it ended.
func TestReadWindow(t *testing.T) {
	tick := fresh(t)
	Record(rec("old", Pass))
	tick(48 * time.Hour)
	cut := now()
	tick(time.Minute)
	Record(rec("new", Fail, "syntax"))
	tick(time.Minute)
	Record(rec("new", Pass))
	rep := Read(cut)
	if rep.Verifications != 2 || rep.Episodes != 1 || rep.Recent[0].Target != "new" {
		t.Fatalf("windowed report = %+v", rep)
	}
}

// An outcome that names no owner, kind or target names nothing and is dropped.
func TestRecordNeedsIdentity(t *testing.T) {
	fresh(t)
	Record(Outcome{Kind: KindTool, Target: "x", Verdict: Pass})
	Record(Outcome{Owner: "u1", Target: "x", Verdict: Pass})
	Record(Outcome{Owner: "u1", Kind: KindTool, Verdict: Pass})
	if rep := Read(time.Time{}); rep.Verifications != 0 {
		t.Fatalf("recorded %d anonymous outcomes", rep.Verifications)
	}
}
