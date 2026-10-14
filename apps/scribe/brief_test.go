package scribe

import (
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/cmcoffee/gohort/core/docs"
)

const testBrief = "Write every section as a general lesson: symptom, where to look, what it means, what to do."

// The brief is set in the Edit dialog, bounded, and left alone by a save that
// does not send it.
func TestABriefIsSetInTheEditDialog(t *testing.T) {
	src, udb, g := guideSourceFixture(t)
	post := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		src.app.handleSettings(w, httptest.NewRequest("POST", "/settings?id="+g.ID, strings.NewReader(body)), udb, "u")
		return w
	}
	if w := post(`{"title":"Dump Triage","brief":"  ` + testBrief + `  "}`); w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	if cur, _ := loadGuide(udb, g.ID); cur.Brief != testBrief {
		t.Fatalf("brief = %q", cur.Brief)
	}
	post(`{"title":"Dump Triage"}`)
	if cur, _ := loadGuide(udb, g.ID); cur.Brief != testBrief {
		t.Error("a save that did not send the brief cleared it")
	}
	if w := post(`{"title":"Dump Triage","brief":"` + strings.Repeat("x", guideBriefMax+1) + `"}`); w.Code != 400 {
		t.Errorf("an over-long brief was accepted: %d", w.Code)
	}
}

// The Curator sees the brief where it decides (the guide list, and a guide's
// sections before it places anything), and is told to decide by it. The
// Author's background placements are told the brief and the user's Rules.
func TestTheBriefReachesTheCuratorAndTheAuthor(t *testing.T) {
	src, udb, g := guideSourceFixture(t)
	g.Brief = testBrief
	saveGuide(udb, g)
	docs.SaveDocRules(udb, rulesNamespace, "Never name a customer.")

	cs := &curatorSession{app: src.app, user: "u", udb: udb}
	if out := cs.listGuides(); !strings.Contains(out, "writing brief: "+testBrief) {
		t.Errorf("list_guides does not show the brief:\n%s", out)
	}
	if out, err := cs.listSections(g.ID); err != nil || !strings.HasPrefix(out, "Writing brief for \"Dump Triage\": "+testBrief) {
		t.Errorf("list_sections does not lead with the brief: %v\n%s", err, out)
	}
	c := src.app.authorBackgroundContext(udb, "u", g.ID)
	if !strings.Contains(c, "Never name a customer.") || !strings.Contains(c, "WRITING BRIEF") || !strings.Contains(c, testBrief) {
		t.Errorf("the Author's background context lacks the Rules or the brief:\n%s", c)
	}

	for file, want := range map[string][]string{
		"coauthor.go": {"T.authorBackgroundContext(udb, user, guideID)"},
		"curator.go":  {"T.authorBackgroundContext(udb, user, guideID)", "A guide may carry a WRITING BRIEF"},
		"web.go":      {"agent.OrchestratorPrompt += guideBriefSection(g)"},
	} {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range want {
			if !strings.Contains(string(src), w) {
				t.Errorf("%s does not carry the brief: missing %q", file, w)
			}
		}
	}
}

// No brief, no section: a guide without one is written exactly as before.
func TestNoBriefAddsNothing(t *testing.T) {
	if s := guideBriefSection(Guide{Brief: "  "}); s != "" {
		t.Errorf("an empty brief produced %q", s)
	}
}
