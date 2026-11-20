package mcpserver

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cmcoffee/gohort/apps/customapps"
	. "github.com/cmcoffee/gohort/core"
)

// askFixture is the apps fixture behind a live server, with the action that
// asks first scoped as an app's is, and an MCP key of user-a's set to ask in
// the client.
type askFixture struct {
	t      *testing.T
	srv    *httptest.Server
	ran    *string
	key    AccountToken
	wipeAt string // the confirm scope of books_wipe
}

func newAskFixture(t *testing.T) *askFixture {
	t.Helper()
	ran := ""
	root := appsFixture(t, &ran)
	scoped := offeringApps
	offeringApps = func(user string) []customapps.OfferingApp {
		apps := scoped(user)
		for i := range apps {
			tools := append([]AgentToolDef(nil), apps[i].Tools...)
			for j := range tools {
				if c := tools[j].Confirmation; c != nil {
					cc := *c
					cc.Scope = "app-action:user-a/" + apps[i].Slug + "/" + tools[j].Tool.Name
					tools[j].Confirmation = &cc
				}
			}
			apps[i].Tools = tools
		}
		return apps
	}
	app := &MCPServer{}
	app.DB = root.Bucket("mcpserver")
	srv := httptest.NewServer(http.HandlerFunc(app.handleApp))
	prevWait := questionWait
	t.Cleanup(func() {
		srv.Close()
		questionWait = prevWait
		askMu.Lock()
		askSessions = map[string]*askSession{}
		askMu.Unlock()
	})
	key := MintAccountTokenScoped("user-a", "Claude Code", &TokenScope{Features: []string{MCPFeatureKey}})
	customapps.SetMCPKeyAsks(key.ID, true)
	return &askFixture{t: t, srv: srv, ran: &ran, key: key, wipeAt: "app-action:user-a/reading/books_wipe"}
}

// post sends one JSON-RPC message to path with key and session.
func (f *askFixture) post(path, key, session string, msg map[string]any, stream bool) *http.Response {
	f.t.Helper()
	body, _ := json.Marshal(msg)
	req, _ := http.NewRequest(http.MethodPost, f.srv.URL+path, strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("X-API-Key", key)
	}
	if session != "" {
		req.Header.Set(sessionHeader, session)
	}
	if stream {
		req.Header.Set("Accept", "application/json, text/event-stream")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	return resp
}

// call is a JSON-RPC request.
func call(method string, params any) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params}
}

// initialize opens a session for key at the reading app when the client
// says it can ask, and returns the session it was given.
func (f *askFixture) initialize(key string, canAsk bool) string {
	f.t.Helper()
	caps := map[string]any{}
	if canAsk {
		caps["elicitation"] = map[string]any{}
	}
	resp := f.post("/app/reading/", key, "", call("initialize", map[string]any{"protocolVersion": "2025-06-18", "capabilities": caps}), false)
	resp.Body.Close()
	return resp.Header.Get(sessionHeader)
}

// body is a plain JSON reply.
func body(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return out
}

// events reads a call's event stream.
type events struct{ r *bufio.Reader }

// next is the stream's next message, failing the test when none comes.
func (e events) next(t *testing.T) map[string]any {
	t.Helper()
	for {
		line, err := e.r.ReadString('\n')
		if err != nil {
			t.Fatalf("the stream ended before a message: %v", err)
		}
		if data, ok := strings.CutPrefix(strings.TrimRight(line, "\n"), "data: "); ok {
			var m map[string]any
			if err := json.Unmarshal([]byte(data), &m); err != nil {
				t.Fatalf("an event that is not JSON: %q", data)
			}
			return m
		}
	}
}

// wipe calls books_wipe on session and returns its stream.
func (f *askFixture) wipe(session string) (*http.Response, events) {
	f.t.Helper()
	resp := f.post("/app/reading/", f.key.Token, session, call("tools/call", map[string]any{"name": "books_wipe", "arguments": map[string]any{"shelf": "all of them"}}), true)
	return resp, events{bufio.NewReader(resp.Body)}
}

// answer posts the client's answer to question on session with key.
func (f *askFixture) answer(key, session string, question any, result map[string]any) int {
	f.t.Helper()
	resp := f.post("/app/reading/", key, session, map[string]any{"jsonrpc": "2.0", "id": question, "result": result}, false)
	resp.Body.Close()
	return resp.StatusCode
}

// A client that says it can ask, on a key set to ask in it, is given a
// session, and only a session lists the action that asks first; another
// client, or another key, is answered as before.
func TestOnlyAClientThatCanAskIsGivenTheActionsThatAskFirst(t *testing.T) {
	f := newAskFixture(t)
	plain := MintAccountTokenScoped("user-a", "script", &TokenScope{Features: []string{MCPFeatureKey}})
	if s := f.initialize(f.key.Token, false); s != "" {
		t.Error("a client that cannot ask was given a session")
	}
	if s := f.initialize(plain.Token, true); s != "" {
		t.Error("a key not set to ask in the client was given a session")
	}
	session := f.initialize(f.key.Token, true)
	if session == "" {
		t.Fatal("a client that can ask, on a key that asks, was given no session")
	}

	list := func(key, session string) map[string]any {
		return body(t, f.post("/app/reading/", key, session, call("tools/list", nil), false))
	}
	if got := toolNames(list(f.key.Token, "")); strings.Join(got, ",") != "books_add" {
		t.Errorf("without its session the list has the action that asks first: %v", got)
	}
	withSession := list(f.key.Token, session)
	if got := toolNames(withSession); strings.Join(got, ",") != "books_add,books_wipe" {
		t.Fatalf("a session lists the action that asks first: %v", got)
	}
	if !strings.Contains(mustJSON(withSession), "Asks you to confirm before it runs.") {
		t.Errorf("the action does not say it asks first: %s", mustJSON(withSession))
	}
	if got := toolNames(list(plain.Token, session)); contains(got, "books_wipe") {
		t.Errorf("another key with the session's id lists the action that asks first: %v", got)
	}

	// Unknown (as after a restart), or a session of another app's address:
	// the call is refused, and nothing runs.
	for _, s := range []string{"", "no-such-session"} {
		res := body(t, f.post("/app/reading/", f.key.Token, s, call("tools/call", map[string]any{"name": "books_wipe"}), true))
		if *f.ran != "" || !strings.Contains(mustJSON(res), "this client cannot be asked") {
			t.Errorf("session %q: ran %q, %s", s, *f.ran, mustJSON(res))
		}
	}
	askMu.Lock()
	askSessions[session].slug = "pantry"
	askMu.Unlock()
	res := body(t, f.post("/app/reading/", f.key.Token, session, call("tools/call", map[string]any{"name": "books_wipe"}), true))
	if *f.ran != "" || !strings.Contains(mustJSON(res), "this client cannot be asked") {
		t.Errorf("another app's session ran the action: ran %q, %s", *f.ran, mustJSON(res))
	}
}

// A call asks the app's own question over what it would do, and runs only on
// a yes; "Don't ask again" on that yes runs it from that key unasked after.
func TestACallAsksAndRunsOnAYes(t *testing.T) {
	f := newAskFixture(t)
	session := f.initialize(f.key.Token, true)
	resp, ev := f.wipe(session)
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("the call is not answered as a stream: %q", ct)
	}
	q := ev.next(t)
	params, _ := q["params"].(map[string]any)
	msg, _ := params["message"].(string)
	if q["method"] != "elicitation/create" || !strings.Contains(msg, "Reading List: books_wipe") || !strings.Contains(msg, "Delete every book?") || !strings.Contains(msg, "- shelf: all of them") {
		t.Fatalf("the question: %s", mustJSON(q))
	}
	if !strings.Contains(mustJSON(params["requestedSchema"]), `"remember"`) {
		t.Errorf("the question offers no \"Don't ask again\": %s", mustJSON(params))
	}
	if *f.ran != "" {
		t.Fatal("the action ran before it was answered")
	}

	// Only the asked key's answer counts.
	other := MintAccountTokenScoped("user-a", "other", &TokenScope{Features: []string{MCPFeatureKey}})
	if code := f.answer(other.Token, session, q["id"], map[string]any{"action": "accept"}); code != http.StatusNotFound {
		t.Errorf("another key's answer: %d", code)
	}
	if code := f.answer(f.key.Token, session, "ask-not-asked", map[string]any{"action": "accept"}); code != http.StatusNotFound {
		t.Errorf("an answer to a question never asked: %d", code)
	}
	if code := f.answer(f.key.Token, session, q["id"], map[string]any{"action": "accept", "content": map[string]any{"remember": true}}); code != http.StatusAccepted {
		t.Fatalf("the answer: %d", code)
	}
	done := ev.next(t)
	if done["id"] != float64(1) || strings.Contains(mustJSON(done), `"isError":true`) || *f.ran != "wiped" {
		t.Fatalf("a yes did not run it: ran %q, %s", *f.ran, mustJSON(done))
	}
	if code := f.answer(f.key.Token, session, q["id"], map[string]any{"action": "accept"}); code != http.StatusNotFound {
		t.Errorf("an answer counted twice: %d", code)
	}
	if !customapps.MCPRunsUnasked(f.key.ID, f.wipeAt) {
		t.Fatal("\"Don't ask again\" kept no yes")
	}

	*f.ran = ""
	res := body(t, f.post("/app/reading/", f.key.Token, session, call("tools/call", map[string]any{"name": "books_wipe"}), true))
	if *f.ran != "wiped" || strings.Contains(mustJSON(res), `"isError":true`) {
		t.Errorf("a remembered yes did not run it unasked: ran %q, %s", *f.ran, mustJSON(res))
	}
}

// Decline, cancel, an error for an answer, or no answer at all: it does not
// run, and the call says why. A yes without "Don't ask again" keeps nothing,
// and neither does one for an action that may not be remembered.
func TestANoOrNoAnswerDoesNotRun(t *testing.T) {
	f := newAskFixture(t)
	session := f.initialize(f.key.Token, true)
	for _, c := range []struct {
		name   string
		answer map[string]any
		want   string
	}{
		{"decline", map[string]any{"jsonrpc": "2.0", "result": map[string]any{"action": "decline"}}, `You declined \"books_wipe\"`},
		{"cancel", map[string]any{"jsonrpc": "2.0", "result": map[string]any{"action": "cancel"}}, `You declined \"books_wipe\"`},
		{"error", map[string]any{"jsonrpc": "2.0", "error": map[string]any{"code": -1, "message": "no dialog here"}}, "could not be asked (no dialog here)"},
	} {
		resp, ev := f.wipe(session)
		q := ev.next(t)
		c.answer["id"] = q["id"]
		if r := f.post("/app/reading/", f.key.Token, session, c.answer, false); r.StatusCode != http.StatusAccepted {
			t.Errorf("%s: answer %d", c.name, r.StatusCode)
		}
		done := ev.next(t)
		resp.Body.Close()
		if *f.ran != "" || !strings.Contains(mustJSON(done), c.want) || !strings.Contains(mustJSON(done), `"isError":true`) {
			t.Errorf("%s: ran %q, %s", c.name, *f.ran, mustJSON(done))
		}
	}

	questionWait = 50 * time.Millisecond
	resp, ev := f.wipe(session)
	ev.next(t)
	done := ev.next(t)
	resp.Body.Close()
	if *f.ran != "" || !strings.Contains(mustJSON(done), "Nobody answered") {
		t.Errorf("no answer: ran %q, %s", *f.ran, mustJSON(done))
	}
	questionWait = time.Minute

	// A yes alone keeps nothing.
	resp, ev = f.wipe(session)
	q := ev.next(t)
	f.answer(f.key.Token, session, q["id"], map[string]any{"action": "accept", "content": map[string]any{"remember": false}})
	ev.next(t)
	resp.Body.Close()
	if *f.ran != "wiped" || customapps.MCPRunsUnasked(f.key.ID, f.wipeAt) {
		t.Errorf("a yes without \"Don't ask again\": ran %q, kept %v", *f.ran, customapps.MCPRunsUnasked(f.key.ID, f.wipeAt))
	}

	// Not to be remembered: no "Don't ask again", and a yes claiming it
	// keeps nothing.
	scoped := offeringApps
	offeringApps = func(user string) []customapps.OfferingApp {
		apps := scoped(user)
		for i := range apps {
			for j := range apps[i].Tools {
				if c := apps[i].Tools[j].Confirmation; c != nil {
					cc := *c
					cc.NeverRemember = true
					apps[i].Tools[j].Confirmation = &cc
				}
			}
		}
		return apps
	}
	resp, ev = f.wipe(session)
	q = ev.next(t)
	if strings.Contains(mustJSON(q), `"remember"`) {
		t.Errorf("an action not to be remembered offers \"Don't ask again\": %s", mustJSON(q))
	}
	f.answer(f.key.Token, session, q["id"], map[string]any{"action": "accept", "content": map[string]any{"remember": true}})
	ev.next(t)
	resp.Body.Close()
	if customapps.MCPRunsUnasked(f.key.ID, f.wipeAt) {
		t.Error("a yes was kept for an action not to be remembered")
	}
}

// A call that cannot take a stream is refused, not asked; a client done with
// its session ends it.
func TestACallWithNoStreamIsRefusedAndASessionEnds(t *testing.T) {
	f := newAskFixture(t)
	session := f.initialize(f.key.Token, true)
	res := body(t, f.post("/app/reading/", f.key.Token, session, call("tools/call", map[string]any{"name": "books_wipe"}), false))
	if *f.ran != "" || !strings.Contains(mustJSON(res), "this client cannot be asked") {
		t.Errorf("a call with no stream: ran %q, %s", *f.ran, mustJSON(res))
	}

	end := func(key, s string) int {
		req, _ := http.NewRequest(http.MethodDelete, f.srv.URL+"/app/reading/", nil)
		req.Header.Set("X-API-Key", key)
		req.Header.Set(sessionHeader, s)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	other := MintAccountTokenScoped("user-a", "other", &TokenScope{Features: []string{MCPFeatureKey}})
	if code := end(other.Token, session); code != http.StatusNotFound {
		t.Errorf("another key ended the session: %d", code)
	}
	if code := end(f.key.Token, session); code != http.StatusNoContent {
		t.Errorf("ending the session: %d", code)
	}
	if got := toolNames(body(t, f.post("/app/reading/", f.key.Token, session, call("tools/list", nil), false))); contains(got, "books_wipe") {
		t.Errorf("an ended session still lists the action that asks first: %v", got)
	}
}

// A key's sessions are capped, the oldest going first, and one unused for a
// day goes when the next is opened.
func TestSessionsAreCappedAndExpire(t *testing.T) {
	askMu.Lock()
	askSessions = map[string]*askSession{}
	askMu.Unlock()
	t.Cleanup(func() {
		askMu.Lock()
		askSessions = map[string]*askSession{}
		askMu.Unlock()
	})
	now := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	first := openAskSession("k", "reading", now)
	for i := 1; i < askSessionsPerKey; i++ {
		openAskSession("k", "reading", now.Add(time.Duration(i)*time.Second))
	}
	other := openAskSession("j", "reading", now)
	openAskSession("k", "reading", now.Add(time.Minute))
	askMu.Lock()
	_, kept := askSessions[first]
	n := 0
	for _, s := range askSessions {
		if s.key == "k" {
			n++
		}
	}
	askMu.Unlock()
	if kept || n != askSessionsPerKey {
		t.Errorf("past the cap: oldest kept %v, %d of the key's sessions", kept, n)
	}
	openAskSession("j", "reading", now.Add(askSessionIdle+2*time.Minute))
	askMu.Lock()
	_, stale := askSessions[other]
	askMu.Unlock()
	if stale {
		t.Error("a session unused for a day was kept")
	}
}

// An answer is handed over once: a second for the same question, even one
// arriving before the call has taken the first, finds nothing waiting and
// is refused at once rather than held.
func TestAnAnswerIsHandedOverOnce(t *testing.T) {
	t.Cleanup(func() {
		askMu.Lock()
		askSessions = map[string]*askSession{}
		askMu.Unlock()
	})
	session := openAskSession("k", "reading", time.Now())
	answers, done := await(session, "ask-1")
	defer done()
	if !deliver(session, "ask-1", askAnswer{Action: "accept"}) {
		t.Fatal("the first answer was not handed over")
	}
	second := make(chan bool, 1)
	go func() { second <- deliver(session, "ask-1", askAnswer{Action: "decline"}) }()
	select {
	case ok := <-second:
		if ok {
			t.Error("a second answer to the same question was handed over")
		}
	case <-time.After(time.Second):
		t.Fatal("a second answer was held instead of refused")
	}
	if a := <-answers; a.Action != "accept" {
		t.Errorf("the answer handed over: %+v", a)
	}
	if deliver("no-such-session", "ask-1", askAnswer{}) {
		t.Error("an answer on an unknown session was handed over")
	}
}
