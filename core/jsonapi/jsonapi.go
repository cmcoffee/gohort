// Package jsonapi is the shape most of gohort's HTTP handlers already had,
// written once: check the method, require a signed-in user, decode the JSON
// body, do the work, encode the JSON reply, and turn a failure into a status.
//
// Hand-written, that frame is twenty lines around two to five of real work,
// and every copy words its errors a little differently. Here a handler is the
// work alone:
//
//	T.HandleFunc("/api/mode", jsonapi.Post(T.DB, func(c *jsonapi.Call, in modeReq) (any, error) {
//		if in.Agent == "" {
//			return nil, jsonapi.BadRequest("agent_id is required")
//		}
//		...
//		return map[string]bool{"ok": true}, nil
//	}))
//
// What it does not cover stays a plain http.HandlerFunc: a stream, an upload,
// a page, a call authenticated by a key rather than the browser session.
//
// Every handler here requires a signed-in user. Same-origin is already
// enforced for every state-changing request by the auth middleware, so it is
// not checked again.
package jsonapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/cmcoffee/gohort/core"
)

// Call is one request as a handler sees it: the request and the writer, for
// the query string and the odd header, and who is calling.
type Call struct {
	W http.ResponseWriter
	R *http.Request
	// User is the signed-in user.
	User string
	// UDB is User's own store under the app's base store.
	UDB core.Database
}

// Error is a failure with the status it should answer with.
type Error struct {
	Code int
	Msg  string
}

func (e *Error) Error() string { return e.Msg }

// Status is a failure answered with code and msg.
func Status(code int, msg string) error { return &Error{Code: code, Msg: msg} }

// BadRequest is a 400: the request is wrong, and msg says how.
func BadRequest(msg string) error { return Status(http.StatusBadRequest, msg) }

// NotFound is a 404.
func NotFound(msg string) error { return Status(http.StatusNotFound, msg) }

// Forbidden is a 403: the caller is signed in and may not do this.
func Forbidden(msg string) error { return Status(http.StatusForbidden, msg) }

// Get answers a GET with what fn returns. Any other method is a 405.
func Get[Out any](base core.Database, fn func(c *Call) (Out, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		c, ok := start(w, r, base)
		if !ok {
			return
		}
		out, err := fn(c)
		finish(w, out, err)
	}
}

// Post answers a request that changes something (POST, PUT, PATCH or DELETE):
// it decodes the JSON body into an In and answers with what fn returns. A safe
// method (GET, HEAD) is a 405, and a body that is not an In is a 400.
//
// An In of struct{} reads no body, for an action the URL says everything
// about (a delete, a switch thrown). By default a missing body is a 400, as a
// malformed one is; EmptyBodyOK takes it as the zero In instead.
//
// Methods picks between Get and Post on a path that answers both.
func Post[In, Out any](base core.Database, fn func(c *Call, in In) (Out, error), opts ...Option) http.HandlerFunc {
	emptyOK := false
	for _, o := range opts {
		emptyOK = emptyOK || o == EmptyBodyOK
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if !core.IsStateChangingMethod(r.Method) {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		c, ok := start(w, r, base)
		if !ok {
			return
		}
		var in In
		if _, none := any(&in).(*struct{}); !none {
			err := json.NewDecoder(r.Body).Decode(&in)
			if err != nil && !(emptyOK && errors.Is(err, io.EOF)) {
				http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
				return
			}
		}
		out, err := fn(c, in)
		finish(w, out, err)
	}
}

// Option changes how Post reads its body.
type Option int

// EmptyBodyOK takes a request with no body as the zero In rather than a 400:
// for an endpoint whose callers leave the body off when the query string
// carries what they mean. A body that is there and malformed is still a 400.
const EmptyBodyOK Option = 1

// Methods routes one path by method, for an endpoint that answers more than
// one: Methods{"GET": jsonapi.Get(...), "POST": jsonapi.Post(...)}.Serve. Any
// http.HandlerFunc can sit in it, so a method whose answer does not fit
// Get or Post keeps a handler of its own beside them.
// A method it has no entry for is a 405.
type Methods map[string]http.HandlerFunc

// Serve is the http.HandlerFunc that routes to m.
func (m Methods) Serve(w http.ResponseWriter, r *http.Request) {
	h, ok := m[r.Method]
	if !ok && r.Method == http.MethodHead {
		h, ok = m[http.MethodGet]
	}
	if !ok {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	h(w, r)
}

// Write answers with v as JSON.
func Write(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// start is the part every handler shares before its work: the signed-in user
// (core.RequireUser answers 401 itself when there is none).
func start(w http.ResponseWriter, r *http.Request, base core.Database) (*Call, bool) {
	user, udb, ok := core.RequireUser(w, r, base)
	if !ok {
		return nil, false
	}
	return &Call{W: w, R: r, User: user, UDB: udb}, true
}

// finish answers with the work's result, or with its failure's status: an
// Error's own, and a 500 for anything else.
func finish[Out any](w http.ResponseWriter, out Out, err error) {
	if err != nil {
		var e *Error
		if errors.As(err, &e) {
			http.Error(w, e.Msg, e.Code)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	Write(w, out)
}
