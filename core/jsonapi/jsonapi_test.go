package jsonapi

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// signIn makes alice a user with a live session and returns the cookie a
// request carries to arrive as her.
func signIn(t *testing.T) *http.Cookie {
	t.Helper()
	adb := &core.DBase{Store: kvlite.MemStore()}
	adb.Set(core.AuthTable, "user:alice", core.AuthUser{Username: "alice"})
	prev := core.AuthDB
	core.AuthDB = func() core.Database { return adb }
	t.Cleanup(func() { core.AuthDB = prev })
	return &http.Cookie{Name: "gohort_session", Value: core.AuthCreateSession(adb, "alice")}
}

func call(h http.HandlerFunc, method, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "/api/x", strings.NewReader(body))
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	h(w, r)
	return w
}

type modeReq struct {
	Agent string `json:"agent_id"`
	Off   bool   `json:"off"`
}

func TestPostDoesTheFrameSoTheHandlerIsTheWork(t *testing.T) {
	cookie := signIn(t)
	base := &core.DBase{Store: kvlite.MemStore()}
	var seen *Call
	h := Post(base, func(c *Call, in modeReq) (any, error) {
		seen = c
		if in.Agent == "" {
			return nil, BadRequest("agent_id is required")
		}
		if in.Agent == "gone" {
			return nil, NotFound("no such agent")
		}
		if in.Agent == "theirs" {
			return nil, Forbidden("not your agent")
		}
		if in.Agent == "broken" {
			return nil, errors.New("the store is down")
		}
		return map[string]any{"agent": in.Agent, "off": in.Off}, nil
	})

	for _, tc := range []struct {
		name, method, body string
		cookie             *http.Cookie
		code               int
		has                string
	}{
		{"not signed in", "POST", `{"agent_id":"a"}`, nil, 401, "unauthorized"},
		{"a safe method", "GET", "", cookie, 405, "method not allowed"},
		{"not JSON", "POST", `{"agent_id":`, cookie, 400, "invalid JSON"},
		{"no body", "POST", "", cookie, 400, "invalid JSON"},
		{"PATCH changes things too", "PATCH", `{"agent_id":"a"}`, cookie, 200, `"agent":"a"`},
		{"the work refuses it", "POST", `{}`, cookie, 400, "agent_id is required"},
		{"not found", "POST", `{"agent_id":"gone"}`, cookie, 404, "no such agent"},
		{"forbidden", "POST", `{"agent_id":"theirs"}`, cookie, 403, "not your agent"},
		{"any other error", "POST", `{"agent_id":"broken"}`, cookie, 500, "details are in its log"},
		{"the work's answer", "POST", `{"agent_id":"a","off":true}`, cookie, 200, `{"agent":"a","off":true}`},
	} {
		w := call(h, tc.method, tc.body, tc.cookie)
		if w.Code != tc.code || !strings.Contains(w.Body.String(), tc.has) {
			t.Errorf("%s: %d %q, want %d containing %q", tc.name, w.Code, w.Body.String(), tc.code, tc.has)
		}
	}
	// An ordinary error's text stays in the log: it can name a path or carry a
	// store's complaint, and a message meant for the person is an Error.
	if w := call(h, "POST", `{"agent_id":"broken"}`, cookie); strings.Contains(w.Body.String(), "store is down") {
		t.Errorf("an internal error's text reached the browser: %q", w.Body.String())
	}
	if w := call(h, "POST", `{"agent_id":"a"}`, cookie); w.Header().Get("Content-Type") != "application/json" {
		t.Errorf("answer is %q, not JSON", w.Header().Get("Content-Type"))
	}
	if seen == nil || seen.User != "alice" || seen.UDB == nil || seen.R == nil || seen.W == nil {
		t.Errorf("the handler was not handed the call: %+v", seen)
	}
}

func TestGetAndMethodsRouteByMethod(t *testing.T) {
	cookie := signIn(t)
	base := &core.DBase{Store: kvlite.MemStore()}
	get := Get(base, func(c *Call) (string, error) { return "read " + c.User, nil })
	if w := call(get, "POST", "{}", cookie); w.Code != 405 {
		t.Errorf("Get answered a POST: %d", w.Code)
	}
	if w := call(get, "GET", "", nil); w.Code != 401 {
		t.Errorf("Get answered a caller who is not signed in: %d", w.Code)
	}

	both := Methods{
		"GET":  get,
		"POST": Post(base, func(c *Call, in modeReq) (string, error) { return "saved " + in.Agent, nil }),
	}.Serve
	for method, want := range map[string]string{"GET": `"read alice"`, "HEAD": `"read alice"`, "POST": `"saved a"`} {
		if w := call(both, method, `{"agent_id":"a"}`, cookie); w.Code != 200 || !strings.Contains(w.Body.String(), want) {
			t.Errorf("%s: %d %q, want %s", method, w.Code, w.Body.String(), want)
		}
	}
	if w := call(both, "DELETE", "", cookie); w.Code != 405 {
		t.Errorf("a method with no entry: %d", w.Code)
	}
}

func TestABodyCanBeLeftOffWhereThatIsMeant(t *testing.T) {
	cookie := signIn(t)
	base := &core.DBase{Store: kvlite.MemStore()}
	lenient := Post(base, func(c *Call, in modeReq) (string, error) {
		return "agent=" + in.Agent + " query=" + c.R.URL.Query().Get("id"), nil
	}, EmptyBodyOK)
	if w := call(lenient, "POST", "", cookie); w.Code != 200 || !strings.Contains(w.Body.String(), "agent=") {
		t.Errorf("EmptyBodyOK refused no body: %d %q", w.Code, w.Body.String())
	}
	if w := call(lenient, "POST", `{"agent_id":`, cookie); w.Code != 400 {
		t.Errorf("EmptyBodyOK took a malformed body: %d", w.Code)
	}

	// An action with no input reads no body at all, so a body that is there
	// (or is not JSON) does not stop it.
	thrown := 0
	action := Post(base, func(c *Call, _ struct{}) (map[string]bool, error) {
		thrown++
		return map[string]bool{"stopped": true}, nil
	})
	for _, body := range []string{"", "not json"} {
		if w := call(action, "DELETE", body, cookie); w.Code != 200 {
			t.Errorf("a struct{} action with body %q: %d %q", body, w.Code, w.Body.String())
		}
	}
	if thrown != 2 {
		t.Errorf("the action ran %d times, want 2", thrown)
	}
}

// A body is read up to a limit, so a caller cannot make the server hold
// whatever it sends.
func TestABodyOverTheLimitIsRefused(t *testing.T) {
	cookie := signIn(t)
	base := &core.DBase{Store: kvlite.MemStore()}
	echo := func(c *Call, in map[string]string) (int, error) { return len(in["text"]), nil }
	big := `{"text":"` + strings.Repeat("x", DefaultMaxBody) + `"}`
	if w := call(Post(base, echo), "POST", big, cookie); w.Code != http.StatusRequestEntityTooLarge || !strings.Contains(w.Body.String(), "over") {
		t.Errorf("a body over the default limit: %d %q", w.Code, w.Body.String())
	}
	if w := call(Post(base, echo, MaxBody(2*DefaultMaxBody)), "POST", big, cookie); w.Code != 200 {
		t.Errorf("MaxBody did not raise the limit: %d %q", w.Code, w.Body.String()[:min(80, w.Body.Len())])
	}
}
