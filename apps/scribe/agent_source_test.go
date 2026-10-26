package scribe

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

func guideSourceFixture(t *testing.T) (guideSource, Database, Guide) {
	t.Helper()
	app := &Scribe{}
	app.DB = testStore(t)
	RegisterDocumentTarget(&guideTarget{app: app})
	udb := UserDB(app.DB, "u")
	g := saveGuide(udb, Guide{ID: "tg", Owner: "u", Title: "Dump Triage", Subtitle: "What a diagnostic dump says", Sections: []Section{
		{ID: "s1", Title: "License server timeouts", Markdown: "A `LIC-TIMEOUT` line in boot.log means the license server is unreachable.\n\nCheck the firewall first.", Order: 1},
		{ID: "s2", Title: "Disk full", Markdown: "When /var fills, the collector writes empty dumps. A timeout is not the cause here.", Order: 2},
		{ID: "s3", Title: "Reading a dump", Markdown: "Start with summary.txt.", Order: 3},
	}})
	return guideSource{app: app}, udb, g
}

func toolsByName(defs []AgentToolDef) map[string]AgentToolDef {
	out := map[string]AgentToolDef{}
	for _, d := range defs {
		out[d.Tool.Name] = d
	}
	return out
}

// A guide attached to an agent is listed among its owner's sources, read as
// a table of contents or a whole section, and searched with the best section
// first.
func TestAnAgentReadsAndSearchesAnAttachedGuide(t *testing.T) {
	src, _, g := guideSourceFixture(t)
	items := src.List("u")
	if len(items) != 1 || items[0].ID != g.ID || items[0].Name != "Dump Triage" || !strings.Contains(items[0].Desc, "3 sections") {
		t.Fatalf("listed %+v", items)
	}
	tools := toolsByName(src.ItemTools("u", g.ID))
	read, search := tools["read_dump_triage_guide"], tools["search_dump_triage_guide"]
	if read.Handler == nil || search.Handler == nil {
		t.Fatalf("tools = %v", tools)
	}
	ctx := context.Background()
	toc, _ := read.Handler(ctx, map[string]any{})
	if !strings.Contains(toc, "1. License server timeouts - A `LIC-TIMEOUT` line") || !strings.Contains(toc, "3. Reading a dump") {
		t.Errorf("contents:\n%s", toc)
	}
	sec, err := read.Handler(ctx, map[string]any{"section": "disk"})
	if err != nil || !strings.Contains(sec, "## Disk full") || !strings.Contains(sec, "empty dumps") {
		t.Errorf("section by part of its title: %v\n%s", err, sec)
	}
	if _, err := read.Handler(ctx, map[string]any{"section": "nothing like it"}); err == nil {
		t.Error("an unknown section should say so")
	}
	found, _ := search.Handler(ctx, map[string]any{"query": "timeout"})
	if i, j := strings.Index(found, "## License server timeouts"), strings.Index(found, "## Disk full"); i < 0 || j < 0 || i > j {
		t.Errorf("the section titled for it should rank first:\n%s", found)
	}
	if none, _ := search.Handler(ctx, map[string]any{"query": "kernel panic"}); !strings.Contains(none, "No section") {
		t.Errorf("no match should say so: %s", none)
	}
}

// An agent reports what it learned into the guide it is attached to: the
// finding is queued for the curator with the guide it was for and the agent
// and conversation it came from. A turn with no agent (a guide's own Author
// reading a sibling guide) is not offered the report tool. Asked with no
// session at all, what the Sources picker asks to decide whether to offer
// "Can make changes", the guide lists it as a tool that writes, and it
// refuses to run that way.
func TestAnAgentReportsAFindingIntoItsGuide(t *testing.T) {
	src, udb, g := guideSourceFixture(t)
	if _, ok := toolsByName(src.ItemToolsWithSession(&ToolSession{Username: "u"}, "u", g.ID))["report_to_dump_triage_guide"]; ok {
		t.Error("a turn with no agent was offered the report tool")
	}
	listed, ok := toolsByName(src.ItemTools("u", g.ID))["report_to_dump_triage_guide"]
	if !ok || len(listed.Tool.Caps) != 1 || listed.Tool.Caps[0] != CapWrite {
		t.Fatalf("the picker cannot see the report tool as one that writes: %+v", listed.Tool)
	}
	if _, err := listed.Handler(context.Background(), map[string]any{"topic": "x", "finding": "y"}); err == nil {
		t.Error("the listed report tool ran with no agent")
	}
	if n := len(listPendingFindings(udb)); n != 0 {
		t.Fatalf("a finding with no agent was queued (%d)", n)
	}
	sess := &ToolSession{AgentID: "a-triage", ChatSessionID: "chat-9"}
	report, ok := toolsByName(src.ItemToolsWithSession(sess, "u", g.ID))["report_to_dump_triage_guide"]
	if !ok {
		t.Fatal("an agent was not offered the report tool")
	}
	out, err := report.Handler(context.Background(), map[string]any{
		"topic": "Empty dumps after an upgrade", "finding": "An upgrade leaves the old collector running; restart it.", "confidence": "probable"})
	if err != nil || !strings.Contains(out, "Filed") {
		t.Fatalf("report: %v %s", err, out)
	}
	pending := listPendingFindings(udb)
	if len(pending) != 1 {
		t.Fatalf("queued %d findings", len(pending))
	}
	f := pending[0]
	if f.Document != g.ID || f.Topic != "Empty dumps after an upgrade" || f.Confidence != ConfidenceProbable ||
		f.Origin.SourceKind != "agent" || f.Origin.ItemID != "a-triage" || f.Origin.RunID != "chat-9" || f.Submitter != "u" {
		t.Errorf("finding = %+v", f)
	}
	if p := curatorBatchPrompt(udb, pending); !strings.Contains(p, "- for guide: Dump Triage (guide_id tg)") {
		t.Errorf("the curator is not told which guide it was for:\n%s", p)
	}
	if _, err := report.Handler(context.Background(), map[string]any{"topic": "x"}); err == nil {
		t.Error("a finding with no content was accepted")
	}
}

// A guide that is gone gives no tools, so a stale attachment is simply not
// there; and another user cannot reach it by its id.
func TestAGoneOrForeignGuideGivesNoTools(t *testing.T) {
	src, udb, g := guideSourceFixture(t)
	if tools := src.ItemTools("someone-else", g.ID); len(tools) != 0 {
		t.Errorf("another user got %d tools for this guide", len(tools))
	}
	deleteGuide(udb, "u", g.ID)
	if tools := src.ItemTools("u", g.ID); len(tools) != 0 {
		t.Errorf("a deleted guide still gave %d tools", len(tools))
	}
}

// The Curator panel lists what is waiting, not only how many: each finding
// with the guide it is for and how long ago it arrived.
func TestTheCuratorListsWhatIsWaiting(t *testing.T) {
	src, _, g := guideSourceFixture(t)
	report := toolsByName(src.ItemToolsWithSession(&ToolSession{AgentID: "a-triage"}, "u", g.ID))["report_to_dump_triage_guide"]
	if _, err := report.Handler(context.Background(), map[string]any{"topic": "Empty dumps", "finding": "Restart the collector."}); err != nil {
		t.Fatal(err)
	}
	w := as(t, src.app, "u", httptest.NewRequest("GET", "/curator/pending", nil))
	body := w.Body.String()
	for _, want := range []string{`"topic":"Empty dumps"`, `"for":"Dump Triage"`, `"age":"just now"`, `"content":"Restart the collector."`} {
		if !strings.Contains(body, want) {
			t.Errorf("the waiting list lacks %s: %s", want, body)
		}
	}
}

// A finding for a guide that curates as findings arrive starts a Curator run
// once reports settle; one for a guide without it waits for the threshold or
// the interval as before.
func TestAGuideCanCurateAsFindingsArrive(t *testing.T) {
	prevSettle, prevMax := curateSettle, curateSettleMax
	curateSettle, curateSettleMax = 20*time.Millisecond, time.Second
	t.Cleanup(func() { curateSettle, curateSettleMax = prevSettle, prevMax })

	src, udb, g := guideSourceFixture(t)
	user := "u-" + t.Name()
	src2 := guideSource{app: src.app}
	udb = UserDB(src.app.DB, user)
	g.Owner = user
	saveGuide(udb, g)
	report := func() {
		t.Helper()
		tool := toolsByName(src2.ItemToolsWithSession(&ToolSession{AgentID: "a-triage"}, user, g.ID))["report_to_dump_triage_guide"]
		if _, err := tool.Handler(context.Background(), map[string]any{"topic": "x", "finding": "y"}); err != nil {
			t.Fatal(err)
		}
	}
	ran := func() bool { _, ok := lastCuratorRun.Load(user); return ok }

	report()
	time.Sleep(100 * time.Millisecond)
	if ran() {
		t.Fatal("a guide without the setting was curated on its first report")
	}

	g.CurateOnReport = true
	saveGuide(udb, g)
	report()
	deadline := time.Now().Add(3 * time.Second)
	for !ran() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !ran() {
		t.Error("a guide that curates as findings arrive was not curated after its report")
	}
}

// The setting lives in the guide's Edit dialog. A save that does not send it
// (a dialog opened before it existed) leaves it as it is.
func TestCurateOnReportIsSetInTheEditDialog(t *testing.T) {
	src, udb, g := guideSourceFixture(t)
	post := func(body string) {
		t.Helper()
		w := as(t, src.app, "u", httptest.NewRequest("POST", "/settings?id="+g.ID, strings.NewReader(body)))
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", body, w.Code, w.Body.String())
		}
	}
	post(`{"title":"Dump Triage","curate_on_report":true}`)
	if cur, _ := loadGuide(udb, g.ID); !cur.CurateOnReport {
		t.Fatal("the setting did not turn on")
	}
	post(`{"title":"Dump Triage"}`)
	if cur, _ := loadGuide(udb, g.ID); !cur.CurateOnReport {
		t.Error("a save that did not send it turned it off")
	}
	w := as(t, src.app, "u", httptest.NewRequest("GET", "/settings?id="+g.ID, nil))
	if !strings.Contains(w.Body.String(), `"curate_on_report":true`) {
		t.Errorf("the dialog is not told it is on: %s", w.Body.String())
	}
}
