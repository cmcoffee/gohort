package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/service"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A flow is edited as rows and "name = value" lines, never JSON: saving
// checks it fits together and refuses it with what is wrong, the row reads
// the flow left to right with what it costs, its form comes back as it was
// written, a new name replaces the old flow, and Test runs it on its sample.
func TestFlowsAreEditedAsRowsAndChecked(t *testing.T) {
	saved := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = saved })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/mpeg")
		w.Write(append([]byte("ID3\x04\x00\x00\x00\x00\x00\x00"), make([]byte, 64)...))
	}))
	defer srv.Close()
	spec, _ := json.Marshal(map[string]any{
		"provider": map[string]any{"transport": "http", "base_url": srv.URL, "auth": map[string]any{"none": true},
			"operations": map[string]any{"speak": map[string]any{"method": "POST", "path": "/speak",
				"body": map[string]any{"text": "{text}"}, "read": "file"}}},
		"provides": []any{map[string]any{"capability": "speech.synthesize", "steps": []any{map[string]any{"call": "speak"}},
			"output": map[string]any{"audio": map[string]any{"from": "body"}}, "cost": map[string]any{"per_call": 0.02}}},
	})
	if err := SaveConnector(RootDB, Connector{Name: "voice", Kind: service.Kind, Spec: spec}); err != nil {
		t.Fatal(err)
	}
	ApproveConnector(RootDB, "voice")

	a := &AdminApp{db: &DBase{Store: kvlite.MemStore()}}
	mux := http.NewServeMux()
	a.registerFlowsRoutes(mux)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		return w
	}
	form := `{"name":"greet","description":"Says hello to someone.",
		"inputs":[{"name":"who","type":"text","required":"yes","description":"Whom to greet."}],
		"steps":[{"id":"say","connector":"voice","capability":"speech.synthesize","with":"text = Hello, {input.who}!"}],
		"test":"who = world"}`

	broken := strings.Replace(form, `{input.who}`, `{input.whom}`, 1)
	if w := call("POST", "/api/flows", broken); w.Code != 400 || !strings.Contains(w.Body.String(), "the flow has no input whom") {
		t.Errorf("a flow that does not fit: %d %s", w.Code, w.Body.String())
	}
	if w := call("POST", "/api/flows", form); w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	var rows []flowRow
	json.Unmarshal(call("GET", "/api/flows", "").Body.Bytes(), &rows)
	if len(rows) != 1 || rows[0].Tool != "flow_greet" || rows[0].Line != "(who) ⇒ speech.synthesize on voice ⇒ (audio)" ||
		rows[0].Status != "Ready. A run costs about $0.0200." || !strings.Contains(rows[0].TestConfirm, "about $0.0200") {
		t.Errorf("rows: %+v", rows)
	}
	var back wiringForm
	json.Unmarshal(call("GET", "/api/flows?name=greet", "").Body.Bytes(), &back)
	if back.Steps[0].With != "text = Hello, {input.who}!" || back.Inputs[0].Required != "yes" || back.Test != "who = world" {
		t.Errorf("the form as written: %+v", back)
	}

	w := call("POST", "/api/flows/test?name=greet", "")
	var res map[string]any
	json.Unmarshal(w.Body.Bytes(), &res)
	if res["ok"] != true || !strings.Contains(res["message"].(string), "say: audio, audio/mpeg") {
		t.Errorf("test: %s", w.Body.String())
	}
	json.Unmarshal(call("POST", "/api/flows/test", broken).Body.Bytes(), &res)
	if res["ok"] != false || !strings.Contains(res["error"].(string), "no input whom") {
		t.Errorf("testing an unsaved form that does not fit: %v", res)
	}

	if w := call("POST", "/api/flows?name=greet", strings.Replace(form, `"greet"`, `"welcome"`, 1)); w.Code != 200 {
		t.Fatalf("rename: %d %s", w.Code, w.Body.String())
	}
	if _, ok := service.GetFlow("greet"); ok {
		t.Error("the old name is still saved")
	}
	call("DELETE", "/api/flows?name=welcome", "")
	if len(service.Flows()) != 0 {
		t.Error("delete left a flow")
	}
}

// A flow's event is set on its form: the monitor as owner:name, the input its
// text goes into, and the runs a day it may start. The row says so, with how
// many it has started today, and the picker lists every user's monitors.
func TestAFlowsEventIsSetOnItsForm(t *testing.T) {
	saved := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = saved })
	spec, _ := json.Marshal(map[string]any{
		"provider": map[string]any{"transport": "http", "base_url": "https://chat.example.test", "auth": map[string]any{"none": true},
			"operations": map[string]any{"post": map[string]any{"method": "POST", "path": "/post", "body": map[string]any{"text": "{text}"}}}},
		"provides": []any{map[string]any{"capability": "message.send", "steps": []any{map[string]any{"call": "post"}}, "output": map[string]any{}}},
	})
	if err := SaveConnector(RootDB, Connector{Name: "room", Kind: service.Kind, Spec: spec}); err != nil {
		t.Fatal(err)
	}
	SaveEventMonitor(RootDB, EventMonitor{Owner: "u1", Name: "news", Kind: EventKindWebhook, WakeAgent: "a1"})
	a := &AdminApp{db: &DBase{Store: kvlite.MemStore()}}
	mux := http.NewServeMux()
	a.registerFlowsRoutes(mux)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		return w
	}
	form := `{"name":"announce","description":"Posts a headline.",
		"inputs":[{"name":"headline","type":"text","required":"yes"}],
		"steps":[{"id":"post","connector":"room","capability":"message.send","with":"text = New: {input.headline}"}],
		"starts":"u1:news","starts_input":"headline","event_runs_per_day":3}`
	if w := call("POST", "/api/flows", form); w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	var back wiringForm
	json.Unmarshal(call("GET", "/api/flows?name=announce", "").Body.Bytes(), &back)
	if back.Starts != "u1:news" || back.StartsInput != "headline" || back.EventRuns != 3 {
		t.Errorf("the form as written: %+v", back)
	}
	var rows []flowRow
	json.Unmarshal(call("GET", "/api/flows", "").Body.Bytes(), &rows)
	if len(rows) != 1 || !strings.Contains(rows[0].Status, "Starts when news (u1) fires, its text into headline: 0 of 3 event runs today.") {
		t.Errorf("row: %+v", rows)
	}
	if opts := monitorOptions(); len(opts) != 2 || opts[1].Value != "u1:news" || opts[0].Value != "" {
		t.Errorf("the monitor picker: %+v", opts)
	}
}
