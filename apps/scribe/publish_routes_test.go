package scribe

// What the publish and curator routes change: Keep published reads back as
// set, a link from a place that is not one of the caller's is not found, Stop
// stops a running publish, the Share picker words Everyone for where the
// guide stands, and Undo puts a guide back as it was before the curator's
// write.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func TestThePublishAndCuratorRoutesChangeWhatTheySay(t *testing.T) {
	prevRoot := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = prevRoot })
	T := &Scribe{AppCore: AppCore{DB: &DBase{Store: kvlite.MemStore()}}}
	udb := UserDB(T.DB, "alice")
	saveGuideRev(udb, Guide{ID: "g1", Title: "Runbook", Owner: "alice", Sections: []Section{{ID: "s1", Title: "One", Markdown: "as written", Order: 1}}}, "Created")
	before := listRevisions(udb, "g1")
	saveGuideRev(udb, Guide{ID: "g1", Title: "Runbook", Owner: "alice", Sections: []Section{{ID: "s1", Title: "One", Markdown: "as the curator wrote it", Order: 1}}}, "Curator placed")
	do := func(method, path, body string) (int, string) {
		t.Helper()
		w := as(t, T, "alice", httptest.NewRequest(method, path, strings.NewReader(body)))
		return w.Code, w.Body.String()
	}

	for _, on := range []bool{true, false} {
		b, _ := json.Marshal(map[string]bool{"on": on})
		if code, body := do("POST", "/publish/keep?id=g1", string(b)); code != http.StatusOK {
			t.Fatalf("keep %v: %d %s", on, code, body)
		}
		if g, _ := loadGuide(udb, "g1"); g.KeepPublished != on {
			t.Errorf("Keep published read back %v, want %v", g.KeepPublished, on)
		}
	}

	if code, _ := do("POST", "/publish/link?id=g1", `{"kind":"nowhere","target":"x","link":"https://example.test/p"}`); code != http.StatusNotFound {
		t.Errorf("a link from a place that is not the caller's answered %d, want 404", code)
	}

	stopped := false
	publishJobsMu.Lock()
	publishJobs["g1"] = &publishJob{owner: "alice", guideID: "g1", cancel: func() { stopped = true }}
	publishJobsMu.Unlock()
	t.Cleanup(func() { publishJobsMu.Lock(); delete(publishJobs, "g1"); publishJobsMu.Unlock() })
	if _, body := do("POST", "/publish/stop?id=g1", ""); !strings.Contains(body, `"stopping":true`) || !stopped {
		t.Errorf("Stop on a running publish: %s, cancelled=%v", body, stopped)
	}

	_, private := do("GET", "/share/candidates?id=g1", "")
	g, _ := loadGuide(udb, "g1")
	g.Shared = true
	saveGuide(udb, g)
	if _, shared := do("GET", "/share/candidates?id=g1", ""); shared == private {
		t.Error("the Share picker words Everyone the same whether or not the guide is shared with everyone")
	}

	saveCuratorRun(udb, CuratorRun{ID: "run1", Owner: "alice", Entries: []CuratorEntry{
		{Kind: OutcomePlaced, FindingID: "f1", Topic: "t", GuideID: "g1", PriorRev: before[len(before)-1].ID}}})
	if code, body := do("POST", "/curator/undo", `{"run_id":"run1","finding_id":"f1"}`); code != http.StatusOK {
		t.Fatalf("undo: %d %s", code, body)
	}
	if g, _ := loadGuide(udb, "g1"); g.Sections[0].Markdown != "as written" {
		t.Errorf("undo left the guide as %q", g.Sections[0].Markdown)
	}
	if code, _ := do("POST", "/curator/undo", `{"run_id":"run1","finding_id":"f1"}`); code != http.StatusBadRequest {
		t.Errorf("undoing it twice answered %d, want 400", code)
	}
}
