package guides

import (
	"path/filepath"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appagents"
)

// testStore returns a throwaway per-user database.
func testStore(t *testing.T) Database {
	t.Helper()
	db, err := OpenDB(filepath.Join(t.TempDir(), "guides.db"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	return db
}

func mkFinding(topic, content, confidence string) DocFinding {
	return DocFinding{
		Content: content, Topic: topic, Confidence: confidence,
		Origin: DocFindingOrigin{SourceKind: "system", ItemID: "app-1", ItemLabel: "web-prod-01"},
	}
}

// TestSubmitFindingNormalizes — a producer that omits confidence must not have
// its guess promoted to a verified fact, because a verified finding is allowed
// to overwrite documented text and a guess is not.
func TestSubmitFindingNormalizes(t *testing.T) {
	udb := testStore(t)
	g := &guideTarget{app: &Guides{}}
	_ = g

	f := mkFinding("nginx tls", "cert renews via certbot", "")
	f.ID = "f1"
	f.Confidence = NormalizeConfidence(f.Confidence)
	if f.Confidence != ConfidenceSingleShot {
		t.Errorf("unstated confidence became %q, want the weakest level", f.Confidence)
	}
	udb.Set(findingsTable, f.ID, f)

	if got := len(listPendingFindings(udb)); got != 1 {
		t.Fatalf("queue has %d findings, want 1", got)
	}
	for _, in := range []string{"verified", "confirmed", "CERTAIN"} {
		if got := NormalizeConfidence(in); got != ConfidenceVerified {
			t.Errorf("NormalizeConfidence(%q) = %q", in, got)
		}
	}
	if got := NormalizeConfidence("who knows"); got != ConfidenceSingleShot {
		t.Errorf("unrecognized confidence = %q, want the weakest level", got)
	}
}

// TestPendingFindingsAreOldestFirst — a later finding that supersedes an earlier
// one has to be applied after it, so the curator reads the queue in arrival
// order rather than whatever the key walk returns.
func TestPendingFindingsAreOldestFirst(t *testing.T) {
	udb := testStore(t)
	for _, tc := range []struct{ id, at string }{
		{"c", "2026-08-11T12:00:02Z"},
		{"a", "2026-08-11T12:00:00Z"},
		{"b", "2026-08-11T12:00:01Z"},
	} {
		f := mkFinding("t", "c", "verified")
		f.ID, f.Submitted = tc.id, tc.at
		udb.Set(findingsTable, f.ID, f)
	}
	got := listPendingFindings(udb)
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("got %d findings, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].ID != want[i] {
			t.Errorf("position %d = %q, want %q", i, got[i].ID, want[i])
		}
	}
}

// TestQueueCapDropsOldest — an unbounded queue means the curator is broken and
// nobody noticed; holding a year of unread findings does not help.
func TestQueueCapDropsOldest(t *testing.T) {
	udb := testStore(t)
	for i := 0; i < maxPendingFindings+10; i++ {
		f := mkFinding("t", "c", "verified")
		f.ID = itoa(i + 1000) // fixed width, so id order matches insert order
		f.Submitted = "2026-08-11T12:00:00Z"
		udb.Set(findingsTable, f.ID, f)
	}
	prunePendingFindings(udb)
	if got := len(listPendingFindings(udb)); got != maxPendingFindings {
		t.Errorf("queue holds %d, want the %d cap", got, maxPendingFindings)
	}
	if _, first := loadFinding(udb, "1000"); first {
		t.Error("the oldest finding survived the cap")
	}
}

func loadFinding(udb Database, id string) (DocFinding, bool) {
	var f DocFinding
	ok := udb.Get(findingsTable, id, &f)
	return f, ok
}

// TestRecordDropsFromQueueButHoldDoesNot — a hold is a deferral, not an outcome.
// Dropping a held finding would silently discard everything the curator could
// not place yet, which is the opposite of what "hold" means.
func TestRecordDropsFromQueueButHoldDoesNot(t *testing.T) {
	udb := testStore(t)
	placed := mkFinding("placed", "x", "verified")
	placed.ID = "p1"
	held := mkFinding("held", "y", "verified")
	held.ID = "h1"
	udb.Set(findingsTable, placed.ID, placed)
	udb.Set(findingsTable, held.ID, held)

	cs := &curatorSession{
		udb:     udb,
		byID:    map[string]DocFinding{"p1": placed, "h1": held},
		decided: map[string]bool{},
	}
	cs.record(CuratorEntry{Kind: OutcomePlaced, FindingID: "p1", GuideID: "g", Section: "S"})
	if _, err := cs.hold("h1", "no guide covers this yet"); err != nil {
		t.Fatalf("hold: %v", err)
	}
	if _, still := loadFinding(udb, "p1"); still {
		t.Error("a placed finding stayed in the queue and will be filed twice")
	}
	if _, still := loadFinding(udb, "h1"); !still {
		t.Error("a HELD finding was dropped from the queue — the deferral discarded it")
	}
	if len(cs.entries) != 2 {
		t.Errorf("recorded %d entries, want 2", len(cs.entries))
	}
}

// TestRecordIgnoresASecondDecision — a duplicate call is a confused curator, and
// its second thought is not obviously better than its first.
func TestRecordIgnoresASecondDecision(t *testing.T) {
	udb := testStore(t)
	f := mkFinding("t", "c", "verified")
	f.ID = "f1"
	udb.Set(findingsTable, f.ID, f)
	cs := &curatorSession{udb: udb, byID: map[string]DocFinding{"f1": f}, decided: map[string]bool{}}

	if msg := cs.record(CuratorEntry{Kind: OutcomePlaced, FindingID: "f1"}); msg != "" {
		t.Fatalf("first decision refused: %s", msg)
	}
	msg := cs.record(CuratorEntry{Kind: OutcomeDiscarded, FindingID: "f1"})
	if msg == "" {
		t.Error("a second decision on the same finding was accepted")
	}
	if len(cs.entries) != 1 || cs.entries[0].Kind != OutcomePlaced {
		t.Errorf("entries = %+v, want only the first decision", cs.entries)
	}
}

// TestRecordCarriesTopicAndOrigin — the digest is read by a human deciding
// whether to undo something, and an entry showing only an opaque finding id
// gives them nothing to judge.
func TestRecordCarriesTopicAndOrigin(t *testing.T) {
	udb := testStore(t)
	f := mkFinding("nginx tls renewal", "certbot", "verified")
	f.ID = "f1"
	udb.Set(findingsTable, f.ID, f)
	cs := &curatorSession{udb: udb, byID: map[string]DocFinding{"f1": f}, decided: map[string]bool{}}
	cs.record(CuratorEntry{Kind: OutcomePlaced, FindingID: "f1"})

	e := cs.entries[0]
	if e.Topic != "nginx tls renewal" {
		t.Errorf("entry topic = %q", e.Topic)
	}
	if e.Origin != "system · web-prod-01" {
		t.Errorf("entry origin = %q", e.Origin)
	}
}

// TestDiscardAndHoldRequireAReason — a discard with no explanation is
// indistinguishable from a bug, and the reason is the only thing that makes the
// decision reviewable.
func TestDiscardAndHoldRequireAReason(t *testing.T) {
	udb := testStore(t)
	f := mkFinding("t", "c", "verified")
	f.ID = "f1"
	udb.Set(findingsTable, f.ID, f)
	cs := &curatorSession{udb: udb, byID: map[string]DocFinding{"f1": f}, decided: map[string]bool{}}

	if _, err := cs.simple(OutcomeDiscarded, "f1", "  ", "Discarded"); err == nil {
		t.Error("a discard with no reason was accepted")
	}
	if _, err := cs.hold("f1", ""); err == nil {
		t.Error("a hold with no reason was accepted")
	}
}

// TestCreateGuideNeedsSeveralFindings — the bound on the curator's authority to
// assert that a topic deserves a document. A spurious near-empty guide looks
// authoritative; a missing one shows up as held findings.
func TestCreateGuideNeedsSeveralFindings(t *testing.T) {
	udb := testStore(t)
	byID := map[string]DocFinding{}
	for i := 0; i < 2; i++ {
		f := mkFinding("t", "c", "verified")
		f.ID = itoa(i)
		byID[f.ID] = f
		udb.Set(findingsTable, f.ID, f)
	}
	cs := &curatorSession{udb: udb, byID: byID, decided: map[string]bool{}, user: "u"}
	_, err := cs.createGuide("Too Thin", []string{"0", "1"})
	if err == nil {
		t.Fatalf("created a guide from %d findings; the floor is %d", len(byID), minFindingsForNewGuide)
	}
	if !strings.Contains(err.Error(), "at least") {
		t.Errorf("error should name the floor: %v", err)
	}
	if _, err := cs.createGuide("", []string{"0", "1", "2"}); err == nil {
		t.Error("created a guide with no title")
	}
}

// TestCuratorRunCounts — the arithmetic that makes a dropped finding visible.
func TestCuratorRunCounts(t *testing.T) {
	run := CuratorRun{
		Findings: 5,
		Entries: []CuratorEntry{
			{Kind: OutcomePlaced}, {Kind: OutcomePlaced},
			{Kind: OutcomeDiscarded}, {Kind: OutcomeHeld},
		},
	}
	c := run.Counts()
	if c[OutcomePlaced] != 2 || c[OutcomeDiscarded] != 1 || c[OutcomeHeld] != 1 {
		t.Errorf("counts = %v", c)
	}
	if run.Unaccounted() != 1 {
		t.Errorf("Unaccounted = %d, want 1 — a finding produced no entry", run.Unaccounted())
	}
	run.Entries = append(run.Entries, CuratorEntry{Kind: OutcomeCreated})
	if run.Unaccounted() != 0 {
		t.Errorf("Unaccounted = %d once every finding has an entry", run.Unaccounted())
	}
}

// TestFindingOriginLabel — the digest names where a claim came from, and an
// unattributed finding says so rather than rendering as an empty gap.
func TestFindingOriginLabel(t *testing.T) {
	cases := []struct {
		in   DocFindingOrigin
		want string
	}{
		{DocFindingOrigin{SourceKind: "system", ItemLabel: "web-prod-01"}, "system · web-prod-01"},
		{DocFindingOrigin{SourceKind: "system", ItemID: "abc"}, "system · abc"},
		{DocFindingOrigin{ItemLabel: "somewhere"}, "somewhere"},
		{DocFindingOrigin{SourceKind: "system"}, "system"},
		{DocFindingOrigin{}, "unattributed"},
	}
	for _, c := range cases {
		if got := findingOriginLabel(c.in); got != c.want {
			t.Errorf("findingOriginLabel(%+v) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestDigestRetentionCap — digests age out, and the newest survive.
func TestDigestRetentionCap(t *testing.T) {
	udb := testStore(t)
	for i := 0; i < maxCuratorRuns+5; i++ {
		saveCuratorRun(udb, CuratorRun{
			ID: itoa(1000 + i), Started: "2026-08-11T12:00:" + pad2(i%60) + "Z", Findings: 1,
		})
	}
	runs := listCuratorRuns(udb)
	if len(runs) > maxCuratorRuns {
		t.Errorf("retained %d digests, want at most %d", len(runs), maxCuratorRuns)
	}
	for i := 1; i < len(runs); i++ {
		if runs[i-1].Started < runs[i].Started {
			t.Fatal("digests are not newest-first")
		}
	}
}

func pad2(n int) string {
	s := itoa(n)
	if len(s) < 2 {
		return "0" + s
	}
	return s
}

// TestStrListTolerance — models emit an array param as both a real array and a
// comma-separated string; rejecting one of them costs a whole decision.
func TestStrListTolerance(t *testing.T) {
	got := strList(map[string]any{"ids": []any{"a", " b ", "", 7}}, "ids")
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("array form = %v", got)
	}
	got = strList(map[string]any{"ids": "a, b ,,c"}, "ids")
	if len(got) != 3 || got[2] != "c" {
		t.Errorf("string form = %v", got)
	}
	if strList(map[string]any{}, "ids") != nil {
		t.Error("a missing param should be nil")
	}
}

// TestCuratorAgentClaimsNoResearchTools — a curator that can search the web
// starts filling the gaps it noticed instead of recording that they exist.
func TestCuratorAgentClaimsNoResearchTools(t *testing.T) {
	spec, ok := appagents.AppAgentByID(curatorAgentID)
	if !ok {
		t.Fatalf("no app agent registered under %q", curatorAgentID)
	}
	{
		if len(spec.AllowedTools) != 0 {
			t.Errorf("the curator declares tools %v; it should have no research surface", spec.AllowedTools)
		}
		for _, want := range []string{"place_finding", "supersede", "flag_contradiction", "discard", "hold", "create_guide"} {
			if !strings.Contains(spec.Prompt, want) {
				t.Errorf("the curator prompt never mentions %q", want)
			}
		}
	}
}
