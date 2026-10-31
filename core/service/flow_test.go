package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// flowWorld is two connectors on one fake provider: voice speaks text
// (priced per call) and ears transcribes audio, saying how much it heard.
func flowWorld(t *testing.T) *[]string {
	t.Helper()
	world(t)
	said := &[]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/speak" {
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			*said = append(*said, body["text"])
			w.Header().Set("Content-Type", "audio/mpeg")
			w.Write(mp3ish)
			return
		}
		if err := r.ParseMultipartForm(8 << 20); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		f, _, err := r.FormFile("file")
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		b, _ := io.ReadAll(f)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"text":"heard ` + string(b[:3]) + `"}`))
	}))
	t.Cleanup(srv.Close)
	install(t, "voice", map[string]any{
		"provider": map[string]any{"transport": "http", "base_url": srv.URL, "auth": map[string]any{"none": true},
			"operations": map[string]any{"speak": map[string]any{"method": "POST", "path": "/speak",
				"body": map[string]any{"text": "{text}"}, "read": "file"}}},
		"provides": []any{map[string]any{"capability": "speech.synthesize", "steps": []any{map[string]any{"call": "speak"}},
			"output": map[string]any{"audio": map[string]any{"from": "body"}}, "cost": map[string]any{"per_call": 0.01}}},
	})
	install(t, "ears", map[string]any{
		"provider": map[string]any{"transport": "http", "base_url": srv.URL, "auth": map[string]any{"none": true},
			"operations": map[string]any{"hear": map[string]any{"method": "POST", "path": "/hear", "send": "multipart",
				"body": map[string]any{"file": "{audio}"}, "read": "json"}}},
		"provides": []any{map[string]any{"capability": "speech.transcribe", "steps": []any{map[string]any{"call": "hear"}},
			"output": map[string]any{"text": map[string]any{"path": "text"}}}},
	})
	return said
}

func roundTrip() Flow {
	return Flow{
		Name:        "round_trip",
		Description: "Says a word and listens to it.",
		Inputs:      []FlowInput{{Name: "word", Type: TypeText, Required: true, Description: "The word to say."}},
		Steps: []FlowStep{
			{ID: "say", Connector: "voice", Capability: "speech.synthesize", With: map[string]string{"text": "Say {input.word} twice"}},
			{ID: "hear", Connector: "ears", Capability: "speech.transcribe", With: map[string]string{"audio": "{say.audio}"}},
		},
		Test: map[string]string{"word": "lighthouse"},
	}
}

// A flow wires one connector's answer into the next one's question, the
// same steps every time, and is a tool an agent can be given: its inputs
// are the tool's parameters and the last step's text is what comes back.
func TestAFlowWiresConnectorsTogether(t *testing.T) {
	said := flowWorld(t)
	f := roundTrip()
	if err := SaveFlow(f); err != nil {
		t.Fatal(err)
	}
	if got := f.Line(); got != "(word) ⇒ speech.synthesize on voice ⇒ speech.transcribe on ears ⇒ (text)" {
		t.Errorf("line: %s", got)
	}
	if est, known := f.Estimate(); known || est != 0.01 {
		t.Errorf("a step with no price leaves the cost unknown, from what is known: %v %v", est, known)
	}
	tool, ok := LookupChatTool("flow_round_trip")
	if !ok {
		t.Fatal("a saved flow is not a tool")
	}
	desc, params := tool.(DynamicChatTool).SchemaWithSession(nil)
	if params["word"].Description != "The word to say. Required." || !strings.Contains(desc, "Says a word and listens to it. Runs the same steps every time: (word)") {
		t.Errorf("schema: %q %+v", desc, params)
	}
	out, err := tool.(SessionChatTool).RunWithSession(map[string]any{"word": "lighthouse"}, &ToolSession{WorkspaceDir: t.TempDir()})
	if err != nil || out != "text: heard ID3" {
		t.Fatalf("run: %q %v", out, err)
	}
	if len(*said) != 1 || (*said)[0] != "Say lighthouse twice" {
		t.Errorf("what was said: %v", *said)
	}
	if rec := RecordOf("voice").Capabilities["speech.synthesize"]; rec.Hours[0].Calls+rec.Hours[1].Calls != 1 {
		t.Errorf("the step's call is counted on its connector: %+v", rec)
	}
	if _, steps, err := TestFlow(context.Background(), f); err != nil || len(steps) != 2 || steps[1].Detail != "text: heard ID3" {
		t.Errorf("test on the samples: %+v %v", steps, err)
	}

	DeleteFlow("round_trip")
	if _, params := tool.(DynamicChatTool).SchemaWithSession(nil); params != nil {
		t.Error("a deleted flow is still offered")
	}
	if _, err := tool.(SessionChatTool).RunWithSession(map[string]any{"word": "x"}, nil); err == nil || !strings.Contains(err.Error(), "removed") {
		t.Errorf("running a deleted flow: %v", err)
	}
}

// A flow that cannot fit together is refused when it is saved, with
// everything that is wrong.
func TestAFlowThatCannotFitIsRefused(t *testing.T) {
	flowWorld(t)
	for want, change := range map[string]func(f *Flow){
		"audio is file, but {input.word} is text":      func(f *Flow) { f.Steps[1].With["audio"] = "{input.word}" },
		"names {hear.text}, which no earlier step":     func(f *Flow) { f.Steps[0].With["text"] = "{hear.text}" },
		"the flow has no input subject":                func(f *Flow) { f.Steps[0].With["text"] = "{input.subject}" },
		"needs audio, and nothing feeds it":            func(f *Flow) { delete(f.Steps[1].With, "audio") },
		"the connector voice does not provide":         func(f *Flow) { f.Steps[1].Connector = "voice" },
		"there is no connector named \"gone\"":         func(f *Flow) { f.Steps[0].Connector = "gone" },
		"takes no input \"loudness\"":                  func(f *Flow) { f.Steps[0].With["loudness"] = "11" },
		"the id say is taken":                          func(f *Flow) { f.Steps[1].ID = "say" },
		"the name must be lower case":                  func(f *Flow) { f.Name = "Round Trip" },
		"say what the flow does":                       func(f *Flow) { f.Description = "" },
		"the test gives volume, which is not an input": func(f *Flow) { f.Test["volume"] = "3" },
	} {
		f := roundTrip()
		change(&f)
		if err := SaveFlow(f); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("want %q, got %v", want, err)
		}
	}
	if len(Flows()) != 0 {
		t.Error("a refused flow was saved")
	}
}

// A flow's last file is saved in the agent's workspace; a file input is a
// workspace file, and a test with none takes its contract's sample.
func TestAFlowTakesAndGivesFiles(t *testing.T) {
	flowWorld(t)
	speak := Flow{Name: "speak_aloud", Description: "Reads text aloud.",
		Inputs: []FlowInput{{Name: "words", Type: TypeText, Required: true}},
		Steps:  []FlowStep{{ID: "say", Connector: "voice", Capability: "speech.synthesize", With: map[string]string{"text": "{input.words}"}}}}
	listen := Flow{Name: "listen", Description: "Writes down what a recording says.",
		Inputs: []FlowInput{{Name: "clip", Type: TypeFile, Required: true}},
		Steps:  []FlowStep{{ID: "hear", Connector: "ears", Capability: "speech.transcribe", With: map[string]string{"audio": "{input.clip}"}}}}
	for _, f := range []Flow{speak, listen} {
		if err := SaveFlow(f); err != nil {
			t.Fatal(err)
		}
	}
	ws := t.TempDir()
	sess := &ToolSession{WorkspaceDir: ws}
	tool, _ := LookupChatTool("flow_speak_aloud")
	out, err := tool.(SessionChatTool).RunWithSession(map[string]any{"words": "hello"}, sess)
	if err != nil || !strings.Contains(out, "saved as speak_aloud-audio.mp3") {
		t.Fatalf("speak: %q %v", out, err)
	}
	if b, err := os.ReadFile(ws + "/speak_aloud-audio.mp3"); err != nil || string(b[:3]) != "ID3" {
		t.Errorf("the audio in the workspace: %v", err)
	}
	tool, _ = LookupChatTool("flow_listen")
	if out, err := tool.(SessionChatTool).RunWithSession(map[string]any{"clip": "speak_aloud-audio.mp3"}, sess); err != nil || out != "text: heard ID3" {
		t.Errorf("listen to a workspace file: %q %v", out, err)
	}
	if _, err := tool.(SessionChatTool).RunWithSession(map[string]any{"clip": "../../etc/passwd"}, sess); err == nil {
		t.Error("a file outside the workspace was read")
	}
	if _, steps, err := TestFlow(context.Background(), listen); err != nil || steps[0].Detail != "text: heard RIF" {
		t.Errorf("a test with no file takes the contract's sample tone: %+v %v", steps, err)
	}
}

// A flow saved while one of its connectors is not approved says so, and a
// run stops at that step with the connector's own reason.
func TestAFlowWaitsOnItsConnectors(t *testing.T) {
	flowWorld(t)
	UnapproveConnector(RootDB, "ears")
	f := roundTrip()
	if err := SaveFlow(f); err != nil {
		t.Fatal(err)
	}
	if w := f.Waiting(); len(w) != 1 || w[0] != "ears" {
		t.Errorf("waiting on: %v", w)
	}
	if _, _, err := f.Run(context.Background(), nil, map[string]any{"word": "x"}); err == nil || !strings.Contains(err.Error(), "step hear (speech.transcribe on ears): service \"ears\" is not approved") {
		t.Errorf("run: %v", err)
	}
	if _, _, err := f.Run(context.Background(), nil, map[string]any{}); err == nil || err.Error() != "round_trip needs word" {
		t.Errorf("a required input left out: %v", err)
	}
}

// poster is a messaging connector giving message.send: a multipart post of
// the text and an optional file, to a room the connector sets. It answers
// with the message's id.
func poster(t *testing.T) *[]string {
	t.Helper()
	got := &[]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(8 << 20); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		line := r.FormValue("to") + ": " + r.FormValue("text")
		if f, _, err := r.FormFile("file"); err == nil {
			b, _ := io.ReadAll(f)
			line += " +" + string(b[:3])
		}
		*got = append(*got, line)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"m-1"}`))
	}))
	t.Cleanup(srv.Close)
	install(t, "poster", map[string]any{
		"provider": map[string]any{"transport": "http", "base_url": srv.URL, "auth": map[string]any{"none": true},
			"operations": map[string]any{"post": map[string]any{"method": "POST", "path": "/post", "send": "multipart",
				"body": map[string]any{"text": "{text}", "to": "{to}", "file": "{file}"}}}},
		"provides": []any{map[string]any{"capability": "message.send", "steps": []any{map[string]any{"call": "post"}},
			"defaults": map[string]any{"to": "room-1"}, "output": map[string]any{"id": map[string]any{"path": "id"}}}},
	})
	return got
}

// A monitor's fire can start a flow, with no agent in between: the event's
// text goes into one input, and the flow sends what it made through a
// message.send step. Only a flow that can run on the event alone and that
// sends is accepted, one per monitor, and its event starts at most so many
// runs a day.
func TestAnEventStartsAFlowThatSends(t *testing.T) {
	said := flowWorld(t)
	sent := poster(t)
	SaveEventMonitor(RootDB, EventMonitor{Owner: "u1", Name: "news", Kind: EventKindWebhook, WakeAgent: "a1"})
	announce := Flow{Name: "announce", Description: "Reads a headline aloud to the room.",
		Inputs: []FlowInput{{Name: "headline", Type: TypeText, Required: true}},
		Steps: []FlowStep{
			{ID: "say", Connector: "voice", Capability: "speech.synthesize", With: map[string]string{"text": "{input.headline}"}},
			{ID: "post", Connector: "poster", Capability: "message.send", With: map[string]string{"text": "New: {input.headline}", "file": "{say.audio}"}},
		},
		Starts: &FlowTrigger{Owner: "u1", Monitor: "news", Input: "headline"}, EventRuns: 2}

	for want, change := range map[string]func(f *Flow){
		"u1 has no monitor by that name":                  func(f *Flow) { f.Starts = &FlowTrigger{Owner: "u1", Monitor: "gone", Input: "headline"} },
		"\"tone\" is not one":                             func(f *Flow) { f.Starts = &FlowTrigger{Owner: "u1", Monitor: "news", Input: "tone"} },
		"the required input voice would never be fed":     func(f *Flow) { f.Inputs = append(f.Inputs, FlowInput{Name: "voice", Type: TypeText, Required: true}) },
		"unless a step sends it: add a message.send step": func(f *Flow) { f.Steps = f.Steps[:1] },
	} {
		f := announce
		f.Inputs = append([]FlowInput(nil), announce.Inputs...)
		change(&f)
		if err := SaveFlow(f); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("want %q, got %v", want, err)
		}
	}
	if err := SaveFlow(announce); err != nil {
		t.Fatal(err)
	}
	other := announce
	other.Name = "echo"
	if err := SaveFlow(other); err == nil || !strings.Contains(err.Error(), "the flow announce already starts when news fires") {
		t.Errorf("a second flow on one event: %v", err)
	}
	if err := SaveFlow(other, "announce"); err != nil {
		t.Errorf("a rename keeps its event: %v", err)
	}
	DeleteFlow("echo")

	f, ok := FlowForMonitor("u1", "news")
	if !ok || f.Name != "announce" {
		t.Fatalf("the flow news starts: %+v %v", f, ok)
	}
	for i := 0; i < 2; i++ {
		if did, err := RunOnEvent(context.Background(), f, "Rates rose"); err != nil || did != "ran say, then post" {
			t.Fatalf("run %d: %q %v", i+1, did, err)
		}
	}
	if len(*sent) != 2 || (*sent)[0] != "room-1: New: Rates rose +ID3" || (*said)[0] != "Rates rose" {
		t.Errorf("sent %v, said %v", *sent, *said)
	}
	if _, err := RunOnEvent(context.Background(), f, "Again"); err == nil || !strings.Contains(err.Error(), "used its 2 event runs for today") {
		t.Errorf("past the day's runs: %v", err)
	}
	if n := EventRunsToday("announce"); n != 2 || len(*sent) != 2 {
		t.Errorf("runs today %d, sent %d", n, len(*sent))
	}
	// The connector's own test sends a sample picture with its text, so a
	// service that sends files by multipart can be tested too.
	if reps, err := Test(context.Background(), "poster"); err != nil || !reps[0].OK || (*sent)[2] != "room-1: A test message from gohort. +\x89PN" {
		t.Errorf("message.send contract test: %+v %v %v", reps, err, *sent)
	}
}
