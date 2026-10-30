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
	r := httptest.NewRequest("POST", "/assist?id=read.heic", strings.NewReader(string(body)))
	r.Header.Set("Accept", "application/x-ndjson") // as uiAssistPost asks
	h(rec, r)
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

// A field too long to show the model whole takes no new value from it: one
// written from the part it saw would replace the whole field and lose the rest.
// The rest of the proposal still fills the form.
func TestAFieldTooLongToShowTakesNoNewValue(t *testing.T) {
	long := strings.Repeat("Keep every line of this. ", maxValue/20)
	fake := &FakeLLM{Turns: []FakeTurn{{Content: "Added a section.\n\n```fields\n{\"instructions\": \"the start [...] and a new section\", \"name\": \"Jira\"}\n```"}}}
	h := Handler(Spec{Route: "test.assist.long", Allow: allowAll, LLM: fake})
	form := []Field{{Field: "name", Label: "Name"}, {Field: "instructions", Label: "Instructions", Type: "textarea"}}
	_, reply, code := post(t, h, Request{Message: "add a section", Form: form, Values: map[string]any{"name": "old", "instructions": long}})
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if reply.Fields["name"] != "Jira" || reply.Fields["instructions"] != nil {
		t.Errorf("fields %+v: want the name and not the instructions", reply.Fields)
	}
	if !strings.Contains(reply.Reply, "Left out instructions") {
		t.Errorf("the reply should say the instructions were left out: %q", reply.Reply)
	}
	if sys := fake.Config(0).SystemPrompt; !strings.Contains(sys, "do not propose a new value for this field") {
		t.Error("the model should be told the field was cut")
	}

	// Under the limit, a long field is shown whole and takes a new value.
	whole := strings.Repeat("Keep every line of this. ", 300)
	fake = &FakeLLM{Turns: []FakeTurn{{Content: "Added.\n\n```fields\n{\"instructions\": \"new\"}\n```"}}}
	h = Handler(Spec{Route: "test.assist.long", Allow: allowAll, LLM: fake})
	_, reply, _ = post(t, h, Request{Message: "add", Form: form, Values: map[string]any{"instructions": whole}})
	if reply.Fields["instructions"] != "new" || !strings.Contains(fake.Config(0).SystemPrompt, whole) {
		t.Errorf("a 7.5KB field should be shown whole and filled: %+v", reply.Fields)
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
		// A skill's instructions hold code fences of their own: the block ends
		// where its JSON does, not at the first fence inside a value.
		{"a value holding a code fence", "Done.\n```fields\n{\"name\": \"Use:\\n```\\n| a | b |\\n```\\nthen stop.\"}\n```\nSave it after.", "Done.\n\nSave it after.", 1, false},
		{"cut off before its end", "Done.\n```fields\n{\"name\": \"Use:\\n```\\n| a", "Done.", 0, true},
	} {
		reply, fields, note := SplitFieldsReply(c.raw, allowed)
		if reply != c.reply || len(fields) != c.fields || (note != "") != c.note {
			t.Errorf("%s: reply %q fields %v note %q", c.name, reply, fields, note)
		}
	}
}

// A file attached in the window is kept for it: the turn it arrives says
// so, later turns list it, and the app's tool reads it without the browser
// sending it again. It is never put in front of the model as content.
func TestAttachedFilesServeTheWindow(t *testing.T) {
	read := ""
	fake := &FakeLLM{Turns: []FakeTurn{
		{Content: "Got it."},
		{ToolCalls: []ToolCall{{ID: "1", Name: "try_it", Args: map[string]any{}}}},
		{Content: "It worked."},
	}}
	spec := Spec{Route: "test.assist.files", Allow: allowAll, LLM: fake, Attach: true,
		Tools: func(*http.Request) []AgentToolDef {
			return []AgentToolDef{{Tool: Tool{Name: "try_it", Caps: []Capability{CapExecute}},
				Handler: func(ctx context.Context, _ map[string]any) (string, error) {
					f, err := FileByID(ctx, "")
					if err != nil {
						return "", err
					}
					read = f.ID + ":" + string(f.Data)
					return "ok", nil
				}}}
		}}
	h := Handler(spec)
	data := "c2FtcGxlIGJ5dGVz" // "sample bytes"
	if _, _, code := post(t, h, Request{Message: "here is one", Window: "w1", Files: []upload{{Name: "photo.heic", Data: data}}}); code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	first := fake.Sent(0)
	if last := first[len(first)-1].Content; !strings.Contains(last, "file-1 (photo.heic") || strings.Contains(last, "sample bytes") || strings.Contains(last, data) {
		t.Errorf("the turn should say what arrived, and never carry it: %q", last)
	}
	if _, _, code := post(t, h, Request{Message: "try it", Window: "w1"}); code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if read != "file-1:sample bytes" {
		t.Errorf("the tool should read the window's file: %q", read)
	}
	if !strings.Contains(fake.Config(1).SystemPrompt, "file-1 (photo.heic") {
		t.Error("later turns should list the window's files")
	}
	// Another window has none of them.
	fake2 := &FakeLLM{Turns: []FakeTurn{{Content: "none"}}}
	spec.LLM = fake2
	post(t, Handler(spec), Request{Message: "x", Window: "w2"})
	if strings.Contains(fake2.Config(0).SystemPrompt, "photo.heic") {
		t.Error("a file leaked into another window")
	}
	// An assist that takes no files refuses them.
	noFiles := Handler(Spec{Route: "test.assist.nofiles", Allow: allowAll, LLM: &FakeLLM{}})
	if _, _, code := post(t, noFiles, Request{Message: "x", Window: "w1", Files: []upload{{Name: "a", Data: data}}}); code != http.StatusBadRequest {
		t.Errorf("files where none are taken: %d", code)
	}
}

// A secret travels as whether it is set and cannot be filled; required
// fields are marked; the page's own words reach the model.
func TestSecretsStayOutAndThePageComesIn(t *testing.T) {
	fake := &FakeLLM{Turns: []FakeTurn{{Content: "Filled.\n```fields\n{\"api_key\": \"guess\", \"host\": \"example.org\"}\n```"}}}
	h := Handler(Spec{Route: "test.assist.secret", Allow: allowAll, LLM: fake})
	_, reply, code := post(t, h, Request{Message: "help", Page: "Connect a mail server: what it needs and why.",
		Form:   []Field{{Field: "host", Required: true}, {Field: "api_key", Type: "password", Secret: true}},
		Values: map[string]any{"api_key": "hunter2-real-key", "host": ""}})
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	sys := fake.Config(0).SystemPrompt
	if strings.Contains(sys, "hunter2") {
		t.Error("a secret's value reached the model")
	}
	for _, want := range []string{"- api_key (password; secret", "Now: (set)", "- host (required)", "Connect a mail server"} {
		if !strings.Contains(sys, want) {
			t.Errorf("the prompt lacks %q:\n%s", want, sys)
		}
	}
	if _, filled := reply.Fields["api_key"]; filled || reply.Fields["host"] != "example.org" {
		t.Errorf("a secret should not be fillable: %+v", reply.Fields)
	}
}

// GET says whether there is a worker to help, so a page offers the ✨ only
// where it can.
func TestAvailableSaysWhetherThereIsAWorker(t *testing.T) {
	prevW, prevL := SharedWorkerLLM(), SharedLeadLLM()
	defer SetSharedLLMs(prevW, prevL)
	h := Handler(Spec{Route: "test.assist.avail", Allow: allowAll})
	get := func() string {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest("GET", "/", nil))
		return strings.TrimSpace(rec.Body.String())
	}
	SetSharedLLMs(nil, nil)
	if got := get(); got != `{"available":false}` {
		t.Errorf("no worker: %s", got)
	}
	SetSharedLLMs(&FakeLLM{}, nil)
	if got := get(); got != `{"available":true}` {
		t.Errorf("a worker: %s", got)
	}
}

// The assists that came before the stream (a field's ✨ Suggest, a document's
// ✨ Draft) ask for plain JSON and get it: one reply, the app's own prompt
// used as written, its own reading of the answer and its own keys beside;
// a request with no message goes to the app's one-shot; a refusal carries
// its status.
func TestAnAppsOwnAssistRunsOnTheHandler(t *testing.T) {
	fake := &FakeLLM{Turns: []FakeTurn{{Content: "Tightened.\n```draft\n## Rules\nBe brief.\n```"}}}
	shot := false
	h := Handler(Spec{Route: "test.assist.own", Allow: allowAll, LLM: fake, Think: Off,
		Prompt: func(r *http.Request, req Request) (string, error) {
			if req.String("field") != "rules" {
				return "", Refuse(http.StatusNotFound, "no field like that")
			}
			return "THE APP'S OWN PROMPT for " + req.String("field") + "\n" + AssistReplyContract(req.Mode), nil
		},
		Parse: func(req Request, raw string) Reply {
			reply, value := SplitDraftReply(raw)
			return Reply{Reply: reply, Value: StripLeadingHeading(value, "Rules"), Extra: map[string]any{"changes": []string{"rules"}}}
		},
		Shot: func(w http.ResponseWriter, r *http.Request, req Request) {
			shot = true
			w.Write([]byte(`{"value":"one shot"}`))
		},
	})
	do := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest("POST", "/", strings.NewReader(body)))
		return rec
	}
	rec := do(`{"field":"rules","message":"tighten","draft":"be brief please","mode":"write"}`)
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("a plain client gets JSON: %q", ct)
	}
	var got map[string]any
	json.Unmarshal(rec.Body.Bytes(), &got)
	if got["reply"] != "Tightened." || got["value"] != "Be brief." || got["changes"] == nil {
		t.Errorf("want the app's reading of the answer, with its own keys: %s", rec.Body.String())
	}
	sys := fake.Config(0).SystemPrompt
	if !strings.HasPrefix(sys, "THE APP'S OWN PROMPT for rules") || strings.Contains(sys, "You are helping someone") || !strings.Contains(sys, DraftReplyContract) {
		t.Errorf("the app's prompt should be used as written:\n%s", sys)
	}
	if think := fake.Config(0).Think; think == nil || *think {
		t.Error("Think: Off should reach the call")
	}
	if do(`{"field":"rules"}`); !shot {
		t.Error("a request with no message should go to the one-shot")
	}
	if rec := do(`{"field":"other","message":"x"}`); rec.Code != http.StatusNotFound {
		t.Errorf("a refusal should carry its status: %d", rec.Code)
	}
}
