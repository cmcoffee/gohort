package assist

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// fakeWeb stands in for the web_search tool, which lives in tools/.
type fakeWeb struct{}

func (fakeWeb) Name() string                       { return "web_search" }
func (fakeWeb) Desc() string                       { return "search the web" }
func (fakeWeb) Params() map[string]ToolParam       { return nil }
func (fakeWeb) Run(map[string]any) (string, error) { return "results", nil }

func init() { RegisterChatTool(fakeWeb{}) }

func allowAll(http.ResponseWriter, *http.Request) bool { return true }

// post sends one turn and reads the answer's lines: the progress, then the
// reply.
func post(t *testing.T, h http.HandlerFunc, req Request) ([]string, Reply, int) {
	t.Helper()
	body, _ := json.Marshal(req)
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest("POST", "/assist?id=read.heic", strings.NewReader(string(body))))
	if rec.Code != http.StatusOK {
		return nil, Reply{}, rec.Code
	}
	var progress []string
	var reply Reply
	sc := bufio.NewScanner(rec.Body)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("a line that is not JSON: %q", sc.Text())
		}
		if p, ok := m["progress"].(string); ok {
			progress = append(progress, p)
			continue
		}
		json.Unmarshal(sc.Bytes(), &reply)
	}
	return progress, reply, rec.Code
}

func toolNames(cfg ChatConfig) []string {
	var out []string
	for _, tl := range cfg.Tools {
		out = append(out, tl.Name)
	}
	return out
}

var heicForm = []Field{
	{Field: "steps", Label: "Steps", Type: "rows", Columns: []Field{{Field: "command"}}},
	{Field: "prefer", Label: "Use before the native flow", Type: "toggle"},
}

// A form turn: the model is told what the work is about and what the form
// holds, uses the app's tool (the window says so while it runs), and what it
// proposes comes back as fields, only the form's own.
func TestAFormTurnUsesTheAppsToolsAndFillsTheForm(t *testing.T) {
	ran := ""
	fake := &FakeLLM{Turns: []FakeTurn{
		{ToolCalls: []ToolCall{{ID: "1", Name: "check_program", Args: map[string]any{"program": "heif-convert"}}}},
		{Content: "heif-convert is installed, so it can do this.\n\n```fields\n{\"steps\": [{\"command\": \"heif-convert {in} {out}\"}], \"owner\": \"x\"}\n```"},
	}}
	h := Handler(Spec{
		Route: "test.assist.form", Label: "Test assist", Allow: allowAll, LLM: fake,
		About: func(r *http.Request) (string, error) {
			return "The job " + r.URL.Query().Get("id") + " cannot run: heif-dec is not installed.", nil
		},
		Tools: func(*http.Request) []AgentToolDef {
			return []AgentToolDef{{
				Tool: Tool{Name: "check_program", Caps: []Capability{CapExecute}},
				Handler: func(_ context.Context, args map[string]any) (string, error) {
					ran, _ = args["program"].(string)
					return "installed", nil
				},
			}}
		},
	})
	progress, reply, code := post(t, h, Request{Message: "fix it", Form: heicForm,
		Values: map[string]any{"steps": []any{map[string]any{"command": "heif-dec {in} {out}"}}}})
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if ran != "heif-convert" {
		t.Errorf("the app's tool did not run: %q", ran)
	}
	if len(progress) != 1 || progress[0] != "check program: heif-convert" {
		t.Errorf("the window should say what it is doing: %q", progress)
	}
	steps, ok := reply.Fields["steps"].([]any)
	if !ok || len(steps) != 1 || reply.Fields["owner"] != nil {
		t.Fatalf("want the proposed steps and nothing the form has no field for: %+v", reply.Fields)
	}
	if !strings.Contains(reply.Reply, "installed") || !strings.Contains(reply.Reply, "owner") {
		t.Errorf("the reply, with a note on what was left out: %q", reply.Reply)
	}
	sys := fake.Config(0).SystemPrompt
	for _, want := range []string{"read.heic cannot run", "- steps (Steps; rows; a list of objects with command)", `heif-dec {in} {out}`, FieldsFence} {
		if !strings.Contains(sys, want) {
			t.Errorf("the system prompt lacks %q:\n%s", want, sys)
		}
	}
	if fake.Config(0).RouteKey != "test.assist.form" || !IsPrivateStage("test.assist.form") {
		t.Error("its calls go by its route, registered as a private worker stage")
	}
}

// The assist proposes and the person saves: an app's tool that writes is
// not given to the model.
func TestAToolThatWritesIsNotGiven(t *testing.T) {
	fake := &FakeLLM{Turns: []FakeTurn{{Content: "ok"}}}
	h := Handler(Spec{Route: "test.assist.write", Allow: allowAll, LLM: fake,
		Tools: func(*http.Request) []AgentToolDef {
			return []AgentToolDef{
				{Tool: Tool{Name: "read_it", Caps: []Capability{CapRead}}, Handler: func(context.Context, map[string]any) (string, error) { return "", nil }},
				{Tool: Tool{Name: "save_it", Caps: []Capability{CapWrite}}, Handler: func(context.Context, map[string]any) (string, error) { return "", nil }},
			}
		}})
	if _, _, code := post(t, h, Request{Message: "hi", Draft: "text"}); code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	got := strings.Join(toolNames(fake.Config(0)), ",")
	if got != "read_it" {
		t.Errorf("tools given: %s", got)
	}
}

// Web lookups are the person's choice per message, and only where the
// assist allows them.
func TestWebLookupsAreOptIn(t *testing.T) {
	for _, c := range []struct {
		name      string
		allow     bool
		ask       bool
		want      bool
		promptHas string
	}{
		{"not asked", true, false, false, ""},
		{"asked and allowed", true, true, true, "turned on web lookups"},
		{"asked, not allowed here", false, true, false, "web lookups are off here"},
	} {
		fake := &FakeLLM{Turns: []FakeTurn{{Content: "ok"}}}
		h := Handler(Spec{Route: "test.assist.web", Allow: allowAll, LLM: fake, Web: c.allow})
		if _, _, code := post(t, h, Request{Message: "look it up", Web: c.ask}); code != http.StatusOK {
			t.Fatalf("%s: status %d", c.name, code)
		}
		has := strings.Contains(strings.Join(toolNames(fake.Config(0)), ","), "web_search")
		if has != c.want {
			t.Errorf("%s: web_search given = %v", c.name, has)
		}
		if c.promptHas != "" && !strings.Contains(fake.Config(0).SystemPrompt, c.promptHas) {
			t.Errorf("%s: the prompt should say %q", c.name, c.promptHas)
		}
	}
}

// A draft turn reads its answer with the draft contract.
func TestADraftTurnProposesText(t *testing.T) {
	fake := &FakeLLM{Turns: []FakeTurn{{Content: "Shorter.\n\n```draft\nnew text\n```"}}}
	h := Handler(Spec{Route: "test.assist.draft", Allow: allowAll, LLM: fake})
	_, reply, code := post(t, h, Request{Message: "shorten", Draft: "old long text", Mode: "write"})
	if code != http.StatusOK || reply.Value != "new text" || reply.Reply != "Shorter." {
		t.Fatalf("%d %+v", code, reply)
	}
	if !strings.Contains(fake.Config(0).SystemPrompt, "old long text") {
		t.Error("the draft should travel to the model")
	}
}

// Refused before the model is asked: no access check, no message, an
// unknown record.
func TestRefusals(t *testing.T) {
	fake := &FakeLLM{}
	if _, _, code := post(t, Handler(Spec{Route: "test.assist.r1", LLM: fake}), Request{Message: "x"}); code != http.StatusForbidden {
		t.Errorf("no access check: %d", code)
	}
	if _, _, code := post(t, Handler(Spec{Route: "test.assist.r2", Allow: allowAll, LLM: fake}), Request{}); code != http.StatusBadRequest {
		t.Errorf("no message: %d", code)
	}
	unknown := Handler(Spec{Route: "test.assist.r3", Allow: allowAll, LLM: fake,
		About: func(*http.Request) (string, error) { return "", Error("no such job") }})
	if _, _, code := post(t, unknown, Request{Message: "x"}); code != http.StatusBadRequest {
		t.Errorf("unknown record: %d", code)
	}
	if fake.Calls() != 0 {
		t.Error("the model was asked anyway")
	}
}

func TestSplitFieldsReply(t *testing.T) {
	allowed := map[string]bool{"name": true, "steps": true}
	for _, c := range []struct {
		name, raw, reply string
		fields           int
		note             bool
	}{
		{"no block", "Just an answer.", "Just an answer.", 0, false},
		{"a proposal", "Done.\n```fields\n{\"name\": \"a\"}\n```", "Done.", 1, false},
		{"text after the block", "Done.\n```fields\n{\"name\": \"a\"}\n```\nSave it after.", "Done.\n\nSave it after.", 1, false},
		{"not JSON", "Done.\n```fields\nname: a\n```", "Done.", 0, true},
		{"a field the form lacks", "Done.\n```fields\n{\"name\": \"a\", \"x\": 1}\n```", "Done.", 1, true},
		{"untagged but plainly the form", "Done.\n```json\n{\"steps\": []}\n```", "Done.", 1, false},
		{"an untagged example", "Like this:\n```json\n{\"other\": 1}\n```", "Like this:\n```json\n{\"other\": 1}\n```", 0, false},
	} {
		reply, fields, note := SplitFieldsReply(c.raw, allowed)
		if reply != c.reply || len(fields) != c.fields || (note != "") != c.note {
			t.Errorf("%s: reply %q fields %v note %q", c.name, reply, fields, note)
		}
	}
}
