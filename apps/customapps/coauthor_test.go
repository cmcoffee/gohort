package customapps

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/agenthost"
)

// A workbench is a co-authored document when its section says editable, as
// authored; an app from before the option, or with no sections kept, is not.
func TestWorkbenchEditable(t *testing.T) {
	sections := func(s string) AppSpec {
		return AppSpec{BodyField: "lyrics", Sections: json.RawMessage(s)}
	}
	for name, c := range map[string]struct {
		spec AppSpec
		want bool
	}{
		"editable":                         {sections(`[{"kind":"workbench","editable":true}]`), true},
		"said as text":                     {sections(`[{"kind":"Workbench","editable":"true"}]`), true},
		"not editable":                     {sections(`[{"kind":"workbench"}]`), false},
		"kind left inferred":               {sections(`[{"item_noun":"song","editable":true}]`), true},
		"another kind":                     {sections(`[{"kind":"html","editable":true}]`), false},
		"quick actions, editable left out": {sections(`[{"kind":"workbench","selection_actions":[{"label":"Tighten"}]}]`), true},
		"quick actions, editable false":    {sections(`[{"kind":"workbench","editable":false,"selection_actions":[{"label":"Tighten"}]}]`), false},
		"said as yes":                      {sections(`[{"kind":"workbench","editable":"yes"}]`), true},
		"no sections kept":                 {AppSpec{BodyField: "lyrics"}, false},
		"not a workbench app":              {AppSpec{Sections: json.RawMessage(`[{"kind":"workbench","editable":true}]`)}, false},
	} {
		if got := workbenchEditable(c.spec); got != c.want {
			t.Errorf("%s: %v, want %v", name, got, c.want)
		}
	}
}

// The turn is told the document as the person has it, how to change it, and
// the passage they selected; nothing when no document is open.
func TestCoauthorTurnContext(t *testing.T) {
	var send coauthorSend
	if coauthorTurnContext(send) != "" {
		t.Error("no document open says nothing")
	}
	send.Document.ID = "song1"
	if got := coauthorTurnContext(send); !strings.Contains(got, "(The document is empty.)") || !strings.Contains(got, "empty find") {
		t.Errorf("an empty document reads as:\n%s", got)
	}
	send.Document.Text = "[Chorus]\nMoney in the cloud\n"
	send.Selection = agenthost.Selection{Text: "Money in the cloud"}
	got := coauthorTurnContext(send)
	for _, want := range []string{"edits not yet saved included", "\n[Chorus]\nMoney in the cloud\n", "propose_edit", "selected this passage"} {
		if !strings.Contains(got, want) {
			t.Errorf("the turn is missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "\u2014") {
		t.Error("no em-dash in prompt text")
	}
}

// A workbench editor saves the id and the edited field only: the record keeps
// everything else, and its key and created stamp cannot be changed that way.
func TestAnEditorSaveMergesIntoTheRecord(t *testing.T) {
	T := sharingTestApp(t)
	spec := AppSpec{Slug: "songs", Name: "Songs", Owner: "u", RecordKey: "id", BodyField: "lyrics"}
	w := appAs(t, T, spec, "u", httptest.NewRequest("POST", "/apps/songs/records",
		strings.NewReader(`{"id":"s1","title":"Money in the Cloud","style":"indie rock","lyrics":"old"}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("seed: %d %s", w.Code, w.Body.String())
	}
	w = appAs(t, T, spec, "u", httptest.NewRequest("POST", "/apps/songs/record?id=s1",
		strings.NewReader(`{"id":"s1","lyrics":"new","created":"1999","title":"Money in the Cloud"}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("patch: %d %s", w.Code, w.Body.String())
	}
	w = appAs(t, T, spec, "u", httptest.NewRequest("GET", "/apps/songs/record?id=s1", nil))
	var rec map[string]any
	json.Unmarshal(w.Body.Bytes(), &rec)
	if rec["lyrics"] != "new" || rec["style"] != "indie rock" || rec["title"] != "Money in the Cloud" {
		t.Errorf("the edited field changes and the rest stays: %v", rec)
	}
	if rec["created"] == "1999" || rec["id"] != "s1" {
		t.Errorf("the key and created stamp are the record's own: %v", rec)
	}
	w = appAs(t, T, spec, "u", httptest.NewRequest("POST", "/apps/songs/record?id=nope", strings.NewReader(`{"lyrics":"x"}`)))
	if w.Code != http.StatusNotFound {
		t.Errorf("a record that is not there is not made by a patch: %d", w.Code)
	}
}

// sendHost stands in for orchestrate on the send a co-authored document
// makes: what the turn was told and which tools it was given.
type sendHost struct {
	agenthost.Host
	turn  string
	tools []string
	pub   *agenthost.Publisher
	body  string
}

func (h *sendHost) PublicHandleSendWithAppToolsPublishing(w http.ResponseWriter, r *http.Request, _ agenthost.Agent, tools []AgentToolDef, pub *agenthost.Publisher) {
	h.turn = agenthost.TurnContext(r.Context())
	for _, td := range tools {
		h.tools = append(h.tools, td.Tool.Name)
	}
	h.pub = pub
	b, _ := io.ReadAll(r.Body)
	h.body = string(b)
}

// The send carries the document and the passage into the turn, leaves the
// body for the chat to read the message from, and gives the agent propose_edit
// beside the app's own tools: never add_section, which would write under the
// person's unsaved edits.
func TestACoauthoredSendProposes(t *testing.T) {
	h := &sendHost{}
	body := `{"message":"tighten it","document":{"id":"s1","text":"Money in the cloud\n"},"selection":{"text":"Money in the cloud","start":0,"end":18}}`
	r := httptest.NewRequest("POST", "/apps/songs/chat/send", strings.NewReader(body))
	other := AgentToolDef{Tool: Tool{Name: "render_song"}}
	var open string
	(&CustomApps{}).handleCoauthorSend(httptest.NewRecorder(), r, h, agenthost.Agent{}, []AgentToolDef{other}, &open)
	if open != "s1" {
		t.Errorf("the open document is the one the send carries: %q", open)
	}
	if !strings.Contains(h.turn, "Money in the cloud") || !strings.Contains(h.turn, "selected this passage") {
		t.Errorf("the turn should carry the document and the passage:\n%s", h.turn)
	}
	if strings.Join(h.tools, ",") != "propose_edit,render_song" {
		t.Errorf("tools %v", h.tools)
	}
	if h.body != body {
		t.Errorf("the chat reads the same body: %q", h.body)
	}
	if h.pub == nil {
		t.Error("propose_edit's cards need the turn's publisher")
	}
}

func (h *sendHost) LookupAppAgent(owner, id string) (agenthost.Agent, bool) {
	return agenthost.Agent{ID: id}, true
}

func (h *sendHost) PublicHandleSendWithAppTools(w http.ResponseWriter, r *http.Request, _ agenthost.Agent, tools []AgentToolDef) {
	h.turn = "(plain send)"
	for _, td := range tools {
		h.tools = append(h.tools, td.Tool.Name)
	}
}

// The chat's send takes the co-authored path for an editable workbench, and a
// plain workbench keeps its add_section.
func TestTheSendFollowsTheWorkbench(t *testing.T) {
	prev := agenthost.Get()
	t.Cleanup(func() { agenthost.Register(prev) })
	send := func(sections string) *sendHost {
		h := &sendHost{}
		agenthost.Register(h)
		spec := AppSpec{Slug: "songs", Owner: "u", AgentID: "lyricist", BodyField: "lyrics", Sections: json.RawMessage(sections)}
		r := httptest.NewRequest("POST", "/apps/songs/chat/send", strings.NewReader(`{"message":"hi"}`))
		(&CustomApps{}).handleChat(httptest.NewRecorder(), r, "u", "u", nil, spec, "send")
		return h
	}
	if h := send(`[{"kind":"workbench","editable":true}]`); strings.Join(h.tools, ",") != "propose_edit" || h.pub == nil {
		t.Errorf("an editable workbench proposes: %v", h.tools)
	}
	if h := send(`[{"kind":"workbench"}]`); strings.Join(h.tools, ",") != "add_section" || h.turn != "(plain send)" {
		t.Errorf("a plain workbench keeps add_section: %v %q", h.tools, h.turn)
	}
	if h := send(`[{"kind":"workbench","editable":true,"files_field":"takes"}]`); strings.Join(h.tools, ",") != "propose_edit,keep_file" {
		t.Errorf("records that keep files give the agent keep_file: %v", h.tools)
	}
	if h := send(`[{"kind":"workbench","files_field":"takes"}]`); strings.Join(h.tools, ",") != "add_section,keep_file" {
		t.Errorf("on a plain workbench too: %v", h.tools)
	}
}
