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
// Every handler here requires a signed-in user, unless it names a gate of its
// own with Authorize. Same-origin is already enforced for every state-changing
// request by the auth middleware, so it is not checked again.
package jsonapi

import (
	"encoding/json"
	"errors"
	"fmt"
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
func Get[Out any](base core.Database, fn func(c *Call) (Out, error), opts ...Option) http.HandlerFunc {
	cfg := configure(opts)
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		c, ok := start(w, r, base, cfg)
		if !ok {
			return
		}
		out, err := fn(c)
		finish(w, r, out, err)
	}
}

// Post answers a POST: it decodes the JSON body into an In and answers with
// what fn returns. Any other method is a 405, and a body that is not an In is
// a 400. Put, Patch and Delete are the same for their own method, so a route
// takes exactly the methods its registration names.
//
// An In of struct{} reads no body, for an action the URL says everything
// about (a delete, a switch thrown). By default a missing body is a 400, as a
// malformed one is; EmptyBodyOK takes it as the zero In instead. A body over
// DefaultMaxBody is a 413; MaxBody raises the limit for an endpoint that takes
// more.
//
// Methods puts more than one of them on a path.
func Post[In, Out any](base core.Database, fn func(c *Call, in In) (Out, error), opts ...Option) http.HandlerFunc {
	return send(http.MethodPost, base, fn, opts)
}

// Put is Post for a PUT.
func Put[In, Out any](base core.Database, fn func(c *Call, in In) (Out, error), opts ...Option) http.HandlerFunc {
	return send(http.MethodPut, base, fn, opts)
}

// Patch is Post for a PATCH.
func Patch[In, Out any](base core.Database, fn func(c *Call, in In) (Out, error), opts ...Option) http.HandlerFunc {
	return send(http.MethodPatch, base, fn, opts)
}

// Delete is Post for a DELETE.
func Delete[In, Out any](base core.Database, fn func(c *Call, in In) (Out, error), opts ...Option) http.HandlerFunc {
	return send(http.MethodDelete, base, fn, opts)
}

// send answers method alone, with its body decoded into an In.
func send[In, Out any](method string, base core.Database, fn func(c *Call, in In) (Out, error), opts []Option) http.HandlerFunc {
	cfg := configure(opts)
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != method {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		c, ok := start(w, r, base, cfg)
		if !ok {
			return
		}
		var in In
		if _, none := any(&in).(*struct{}); !none {
			err := json.NewDecoder(http.MaxBytesReader(w, r.Body, cfg.maxBody)).Decode(&in)
			var tooBig *http.MaxBytesError
			switch {
			case errors.As(err, &tooBig):
				http.Error(w, fmt.Sprintf("request body is over %d bytes", tooBig.Limit), http.StatusRequestEntityTooLarge)
				return
			case err != nil && !(cfg.emptyOK && errors.Is(err, io.EOF)):
				http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
				return
			}
		}
		out, err := fn(c, in)
		finish(w, r, out, err)
	}
}

// Option changes how a handler admits its caller or reads its body.
type Option func(*config)

type config struct {
	authorize  func(http.ResponseWriter, *http.Request) bool
	internalOK bool
	emptyOK    bool
	maxBody    int64
}

func configure(opts []Option) config {
	cfg := config{maxBody: DefaultMaxBody}
	for _, o := range opts {
		o(&cfg)
	}
	return cfg
}

// Authorize admits the caller by check instead of by a signed-in user: for a
// surface with a gate of its own, such as the admin pages, which an install
// with no accounts yet opens to whoever reaches it on a safe address. check
// answers the request itself when it refuses (a 403, a 404). The Call's User
// is whoever is signed in, "" when nobody is, and its UDB is then nil.
func Authorize(check func(http.ResponseWriter, *http.Request) bool) Option {
	return func(c *config) { c.authorize = check }
}

// InternalOK also admits this instance's own internal request
// (core.NewInternalRequest), which has no user behind it: for an endpoint
// another app's background work calls, such as a scheduler asking for
// suggestions. It is the same proof the auth middleware takes, a per-process
// token on a genuinely local connection that is not a browser. The Call's
// User is then "" and its UDB nil, so a handler that keys a store by the
// caller must not take this.
var InternalOK Option = func(c *config) { c.internalOK = true }

// DefaultMaxBody is the most a Post reads unless MaxBody says otherwise: room
// for any form or record, and a ceiling on what a caller can make the server
// hold.
const DefaultMaxBody = 1 << 20

// EmptyBodyOK takes a request with no body as the zero In rather than a 400:
// for an endpoint whose callers leave the body off when the query string
// carries what they mean. A body that is there and malformed is still a 400.
var EmptyBodyOK Option = func(c *config) { c.emptyOK = true }

// MaxBody lets a Post read up to n bytes, for an endpoint whose body is a
// document rather than a form.
func MaxBody(n int64) Option { return func(c *config) { c.maxBody = n } }

// Methods routes one path by method, for an endpoint that answers more than
// one: Methods{"GET": jsonapi.Get(...), "DELETE": jsonapi.Delete(...)}.Serve.
// Any http.HandlerFunc can sit in it, so a method whose answer does not fit
// these keeps a handler of its own beside them.
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

// Empty is a reply of code and no body, for a handler whose answer is the
// status: a 204 for a change with nothing to report, a 202 for work started in
// the background. Return it as the handler's result.
func Empty(code int) any { return emptyReply(code) }

type emptyReply int

// Write answers with v as JSON.
func Write(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// start is the part every handler shares before its work: the caller's gate,
// Authorize's check or else a signed-in user (core.RequireUser answers 401
// itself when there is none).
func start(w http.ResponseWriter, r *http.Request, base core.Database, cfg config) (*Call, bool) {
	if cfg.internalOK && core.IsInternalRequest(r) && !core.IsBrowserRequest(r) {
		return &Call{W: w, R: r}, true
	}
	if cfg.authorize != nil {
		if !cfg.authorize(w, r) {
			return nil, false
		}
		c := &Call{W: w, R: r, User: core.AuthCurrentUser(r)}
		if c.User != "" {
			c.UDB = core.UserDB(base, c.User)
		}
		return c, true
	}
	user, udb, ok := core.RequireUser(w, r, base)
	if !ok {
		return nil, false
	}
	return &Call{W: w, R: r, User: user, UDB: udb}, true
}

// finish answers with the work's result, or with its failure's status: an
// Error's own, with its message, and a 500 for anything else. A plain error's
// text goes to the log, not the browser: it can carry a path, a store's
// complaint or a peer's reply, and a handler that means a message for the
// person returns it as an Error.
func finish[Out any](w http.ResponseWriter, r *http.Request, out Out, err error) {
	if err != nil {
		var e *Error
		if errors.As(err, &e) {
			http.Error(w, e.Msg, e.Code)
			return
		}
		core.Err("[jsonapi] %s %s: %v", r.Method, r.URL.Path, err)
		http.Error(w, "something went wrong on the server; the details are in its log", http.StatusInternalServerError)
		return
	}
	if code, ok := any(out).(emptyReply); ok {
		w.WriteHeader(int(code))
		return
	}
	Write(w, out)
}
