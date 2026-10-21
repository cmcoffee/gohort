// Package assist is the ✨ assist's server half: one conversation turn with
// the worker about one thing someone is working on, a draft or a form, which
// sits beside the conversation (ui.AssistPanel, or uiOpenAssist with a url).
//
// An app gives what only it knows (Spec): who may use it, what the work is
// about (the job the form fills, why it fails), and its own tools (try the
// form's values, read a program's help). The rest is here, once: the model
// (the shared worker, on a route the app names, which is registered here so
// it cannot be forgotten), the tool loop, web lookups when the person turns
// them on, saying what it is doing while it works, and the reply contract
// with its parser, so the instruction the model gets and the code that reads
// its answer cannot drift apart.
//
// THE ASSIST PROPOSES, IT DOES NOT SAVE. What it suggests fills the draft or
// the form, and the person keeps it with the work's own Save, which is the
// app's endpoint and does the app's own checks. So an app's tool that writes
// (CapWrite) is not given to the model here: it is dropped, and the drop is
// logged. Looking things up on the web is the person's choice per message,
// and never in private mode.
//
// THE PROTOCOL. The request is JSON: {message, history, mode, web, window}
// with either {draft} or {form, values}, and {files: [{name, data}]} on the
// turn a file is attached (Spec.Attach; see files.go); form is the browser's description of
// the fields, which only shapes what the model is told and which fields a
// proposal may fill, never what is saved. The answer is a stream of JSON
// lines: {"progress": "..."} while it works, then one {"reply", "value"}
// (a draft) or {"reply", "fields"} (a form), or {"error"}.
package assist

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// Spec is one assist.
type Spec struct {
	// Route is the routing key its model calls go by ("app.admin.hooks_
	// assist"); Label and App describe it on the routing page. It is
	// registered as a private worker stage when the handler is built.
	Route, Label, App string
	// Allow gates the request, writing the refusal itself. Required: a
	// Spec without one refuses everyone.
	Allow func(w http.ResponseWriter, r *http.Request) bool
	// About says what the work is about, from the request (its URL names
	// the record): what it is for, what it holds, what is wrong with it.
	// An error refuses the request with its text.
	About func(r *http.Request) (string, error)
	// Tools are the app's own for this work. One that writes is dropped.
	Tools func(r *http.Request) []AgentToolDef
	// Web lets the person turn on web lookups (web_search, fetch_url).
	Web bool
	// Attach lets the person add files in the window, for the app's tools
	// (Files): never shown to the model.
	Attach bool
	// LLM is the model, for a test; nil is the shared worker.
	LLM LLM
	// Core is the app's own model handle (its worker, its name on the usage
	// report), where it has one; nil is the shared worker.
	Core *AppCore

	// Prompt writes the whole system prompt itself, for an assist whose
	// instructions are its own (a field of an agent's, a step of a
	// workflow's): used as written, with only what this turn adds (web
	// lookups, attached files) after it. About and the form or draft
	// sections are not added. An error refuses the request (Refuse for its
	// status).
	Prompt func(r *http.Request, req Request) (string, error)
	// Parse reads the model's answer the app's way (a draft cleaned for
	// its field, a list of changes); nil reads the draft or fields
	// contract.
	Parse func(req Request, raw string) Reply
	// Shot answers a request with no message: an app's one-shot
	// suggestion. nil refuses it.
	Shot func(w http.ResponseWriter, r *http.Request, req Request)
	// Think, when set, pins thinking on or off for its calls.
	Think *bool
	// Timeout bounds a turn; 0 is the request's own.
	Timeout time.Duration
}

// Refusal is a refusal with the status it carries: from About, Prompt or a
// Shot, as Refuse makes it.
type Refusal struct {
	Code int
	Msg  string
}

func (e Refusal) Error() string { return e.Msg }

// Refuse is a refusal with status code.
func Refuse(code int, msg string) error { return Refusal{Code: code, Msg: msg} }

// WriteError writes err as its status: a Refusal's own, 400 for a plain
// error. For a Shot that refuses the way the handler does.
func WriteError(w http.ResponseWriter, err error) {
	var rf Refusal
	if errors.As(err, &rf) {
		http.Error(w, rf.Msg, rf.Code)
		return
	}
	http.Error(w, err.Error(), http.StatusBadRequest)
}

// Off is false, for Spec.Think.
var Off = func() *bool { v := false; return &v }()

// Field describes one field of the form beside the conversation.
type Field struct {
	Field   string   `json:"field"`
	Label   string   `json:"label,omitempty"`
	Type    string   `json:"type,omitempty"`
	Help    string   `json:"help,omitempty"`
	Detail  string   `json:"detail,omitempty"`
	Options []string `json:"options,omitempty"`
	Columns []Field  `json:"columns,omitempty"`
	// Required fields are what the form cannot be saved without. A Secret
	// one (a password, a key) travels as whether it is set, never its value,
	// and a proposal cannot fill it.
	Required bool `json:"required,omitempty"`
	Secret   bool `json:"secret,omitempty"`
}

// Turn is one message of the conversation so far.
type Turn struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Request is one turn.
type Request struct {
	Message string   `json:"message"`
	History []Turn   `json:"history"`
	Mode    string   `json:"mode"`
	Web     bool     `json:"web"`
	Window  string   `json:"window"`
	Files   []upload `json:"files"`
	Draft   string   `json:"draft"`
	// Page is where the work is, in the page's own words (its section's
	// title and description): from the browser, so it shapes the help and
	// nothing else.
	Page   string         `json:"page"`
	Form   []Field        `json:"form"`
	Values map[string]any `json:"values"`

	// raw is every key the request carried, for an app's own (Get).
	raw map[string]json.RawMessage
}

// Get decodes the request's own key (an app's: field, section, record)
// into v, and reports whether it was there and read.
func (q Request) Get(key string, v any) bool {
	raw, ok := q.raw[key]
	if !ok {
		return false
	}
	return json.Unmarshal(raw, v) == nil
}

// String is the request's own key as a string, trimmed: "" when it is not
// one.
func (q Request) String(key string) string {
	var s string
	q.Get(key, &s)
	return strings.TrimSpace(s)
}

// Reply is the turn's answer.
type Reply struct {
	Reply  string         `json:"reply"`
	Value  string         `json:"value,omitempty"`
	Fields map[string]any `json:"fields,omitempty"`
	Error  string         `json:"error,omitempty"`
	// Extra is an app's own keys beside these (a list of changes).
	Extra map[string]any `json:"-"`
}

// MarshalJSON writes the reply with its Extra keys beside the rest.
func (rp Reply) MarshalJSON() ([]byte, error) {
	type plain Reply
	base, err := json.Marshal(plain(rp))
	if err != nil || len(rp.Extra) == 0 {
		return base, err
	}
	var m map[string]any
	if err := json.Unmarshal(base, &m); err != nil {
		return nil, err
	}
	for k, v := range rp.Extra {
		if _, taken := m[k]; !taken {
			m[k] = v
		}
	}
	return json.Marshal(m)
}

// Bounds on what a turn carries to the model.
const (
	maxBody    = 1 << 20
	maxDraft   = 32 << 10
	maxValue   = 4 << 10
	maxHistory = 30
	maxPage    = 4 << 10
)

// Handler is the assist's endpoint.
func Handler(s Spec) http.HandlerFunc {
	if s.Route == "" {
		s.Route = "assist"
	}
	RegisterRouteStage(RouteStage{Key: s.Route, Label: firstNonEmpty(s.Label, "Assist"), Default: "worker", Private: true, App: s.App})
	return func(w http.ResponseWriter, r *http.Request) {
		if s.Allow == nil {
			http.Error(w, "this assist has no access check", http.StatusForbidden)
			return
		}
		if !s.Allow(w, r) {
			return
		}
		// GET says whether help is there to be had: a worker to give it. A
		// page asks before it shows the ✨, so a deployment with no worker
		// never offers what it cannot do.
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			json.NewEncoder(w).Encode(map[string]bool{"available": s.LLM != nil || SharedWorkerLLM() != nil})
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		limit := int64(maxBody)
		if s.Attach {
			limit = attachBody
		}
		var req Request
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
		if err != nil || json.Unmarshal(body, &req) != nil || json.Unmarshal(body, &req.raw) != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if len(req.Files) > 0 && !s.Attach {
			http.Error(w, "this assist takes no files", http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(req.Message) == "" {
			if s.Shot != nil {
				s.Shot(w, r, req)
				return
			}
			http.Error(w, "message required", http.StatusBadRequest)
			return
		}
		about, prompt := "", ""
		if s.Prompt != nil {
			if prompt, err = s.Prompt(r, req); err != nil {
				WriteError(w, err)
				return
			}
		} else if s.About != nil {
			if about, err = s.About(r); err != nil {
				WriteError(w, err)
				return
			}
		}
		llm := s.LLM
		if llm == nil && s.Core != nil {
			llm = s.Core.LLM
		}
		if llm == nil {
			llm = SharedWorkerLLM()
		}
		if llm == nil {
			http.Error(w, "no worker LLM is configured", http.StatusServiceUnavailable)
			return
		}

		user := AuthCurrentUser(r)
		key := windowKey(user, s.Route, req.Window)
		var arrived []string
		if len(req.Files) > 0 {
			var err error
			if arrived, err = keep(key, req.Files); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
		files := filesOf(key)

		out := newStream(w, strings.Contains(r.Header.Get("Accept"), "application/x-ndjson"))
		t := turn{spec: s, req: req, about: about, prompt: prompt, user: user, out: out, files: files, arrived: arrived}
		ctx := WithFiles(r.Context(), files)
		if s.Timeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, s.Timeout)
			defer cancel()
		}
		reply, err := t.run(ctx, llm, s.tools(r))
		if err != nil {
			out.fail("assist failed: " + err.Error())
			return
		}
		out.final(reply)
	}
}

// tools are the app's tools the model may have here: those that write are
// dropped, with a breadcrumb.
func (s Spec) tools(r *http.Request) []AgentToolDef {
	if s.Tools == nil {
		return nil
	}
	var kit []AgentToolDef
	for _, def := range s.Tools(r) {
		if hasCap(def.Tool.Caps, CapWrite) {
			Debug("[assist] %s: tool %s writes, so it is not given to the model; the assist proposes and the person saves", s.Route, def.Tool.Name)
			continue
		}
		kit = append(kit, def)
	}
	return kit
}

// turn is one request's work.
type turn struct {
	spec  Spec
	req   Request
	about string
	// prompt is the app's own system prompt, when it writes one.
	prompt string
	user   string
	out    *stream
	// files are the window's attachments; arrived, the labels of those
	// this turn brought.
	files   []File
	arrived []string
}

func (t turn) run(ctx context.Context, llm LLM, kit []AgentToolDef) (Reply, error) {
	web := t.webState()
	if web == webOn {
		for _, name := range []string{"web_search", "fetch_url"} {
			if ct, ok := FindChatTool(name); ok {
				kit = append(kit, ChatToolToAgentToolDef(ct))
			}
		}
	}
	for i := range kit {
		kit[i].Handler = t.reporting(kit[i])
	}
	msgs := t.messages()
	ac := t.spec.Core
	if ac == nil || ac.LLM == nil {
		ac = &AppCore{LLM: llm}
	}
	opts := []ChatOption{WithSystemPrompt(t.system(kit, web)), WithRouteKey(t.spec.Route)}
	if t.spec.Think != nil {
		opts = append(opts, WithThink(*t.spec.Think))
	}
	resp, err := ac.WorkerChatWithTools(ctx, msgs, kit, opts...)
	if err != nil {
		return Reply{}, err
	}
	if resp == nil {
		return Reply{}, errors.New("empty response")
	}
	raw := ResponseText(resp)
	if t.spec.Parse != nil {
		return t.spec.Parse(t.req, raw), nil
	}
	if len(t.req.Form) > 0 {
		reply, fields, note := SplitFieldsReply(raw, t.fieldNames())
		if note != "" {
			reply = strings.TrimSpace(reply + "\n\n" + note)
		}
		return Reply{Reply: reply, Fields: fields}, nil
	}
	reply, value := SplitDraftReply(raw)
	return Reply{Reply: reply, Value: value}, nil
}

// The three ways web lookups can stand for a turn.
const (
	webOff     = iota // not asked for
	webOn             // asked for and allowed
	webRefused        // asked for, and this person or this assist cannot
)

func (t turn) webState() int {
	if !t.req.Web {
		return webOff
	}
	if !t.spec.Web || (t.user != "" && AuthGetPrivateMode(AuthDB(), t.user)) {
		return webRefused
	}
	return webOn
}

// reporting wraps a tool so the window says what it is doing while it runs.
func (t turn) reporting(def AgentToolDef) ToolHandlerFunc {
	run := def.Handler
	name := def.Tool.Name
	return func(ctx context.Context, args map[string]any) (string, error) {
		t.out.send(map[string]string{"progress": progressLabel(name, args)})
		return run(ctx, args)
	}
}

// progressLabel is what a tool call reads as in the window: its name in
// words, and the first short thing it was given (the program, the query).
func progressLabel(name string, args map[string]any) string {
	label := strings.ReplaceAll(name, "_", " ")
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if s, ok := args[k].(string); ok && strings.TrimSpace(s) != "" {
			s = strings.Join(strings.Fields(s), " ")
			if len([]rune(s)) > 60 {
				s = string([]rune(s)[:60]) + "…"
			}
			return label + ": " + s
		}
	}
	return label
}

// messages are the conversation so far and this turn's message.
func (t turn) messages() []Message {
	hist := t.req.History
	if len(hist) > maxHistory {
		hist = hist[len(hist)-maxHistory:]
	}
	msgs := make([]Message, 0, len(hist)+1)
	for _, h := range hist {
		role := strings.TrimSpace(h.Role)
		if (role != "user" && role != "assistant") || strings.TrimSpace(h.Content) == "" {
			continue
		}
		msgs = append(msgs, Message{Role: role, Content: h.Content})
	}
	msg := t.req.Message
	if len(t.arrived) > 0 {
		msg = "[I attached " + strings.Join(t.arrived, "; ") + ".]\n\n" + msg
	}
	return append(msgs, Message{Role: "user", Content: msg})
}

// system is the turn's system prompt: the role, what the work is about, the
// work as it stands, what it may use, and how to answer.
func (t turn) system(kit []AgentToolDef, web int) string {
	var b strings.Builder
	if t.prompt != "" {
		// The app's own instructions, as written; only what this turn
		// brings is added after them.
		b.WriteString(t.prompt)
		t.writeTurnExtras(&b, web)
		return b.String()
	}
	b.WriteString("You are helping someone in gohort with one piece of work, which is shown beside this conversation. Find things out with the tools you have rather than guessing, say plainly what you found, and keep replies short.\n")
	if strings.TrimSpace(t.about) != "" {
		b.WriteString("\n## What this is about\n\n")
		b.WriteString(strings.TrimSpace(t.about))
		b.WriteString("\n")
	}
	if page := strings.TrimSpace(t.req.Page); page != "" {
		if len(page) > maxPage {
			page = page[:maxPage]
		}
		b.WriteString("\n## Where this is, in the page's own words\n\n")
		b.WriteString(page)
		b.WriteString("\n")
	}
	if len(t.req.Form) > 0 {
		b.WriteString("\n## The form, as it stands now\n\n")
		for _, f := range t.req.Form {
			writeField(&b, f, t.req.Values[f.Field])
		}
	} else {
		b.WriteString("\n## The draft, as it stands now\n\n")
		d := t.req.Draft
		if len(d) > maxDraft {
			d = d[:maxDraft] + "\n[... the rest is not shown]"
		}
		if strings.TrimSpace(d) == "" {
			d = "(empty)"
		}
		b.WriteString(d)
		b.WriteString("\n")
	}
	t.writeTurnExtras(&b, web)
	if len(kit) == 0 {
		b.WriteString("\nYou have no tools in this conversation: work from what is above and what the person says.\n")
	}
	b.WriteString("\n## How to reply\n\n")
	if len(t.req.Form) > 0 {
		b.WriteString(FieldsReplyContract)
	} else {
		b.WriteString(AssistReplyContract(t.req.Mode))
	}
	return b.String()
}

// writeTurnExtras is what this turn brings to the prompt: the files
// attached in the window, and web lookups asked for.
func (t turn) writeTurnExtras(b *strings.Builder, web int) {
	if len(t.files) > 0 {
		b.WriteString("\n## Files attached here\n\n")
		for _, f := range t.files {
			b.WriteString("- " + f.Label() + "\n")
		}
		b.WriteString("They are not shown to you: a tool that takes a file uses one by its id, the latest when none is given.\n")
	}
	switch web {
	case webOn:
		b.WriteString("\n## The web\n\nThe person has turned on web lookups: you may search the web (web_search) and read pages (fetch_url). Prefer what you can find out here, with your other tools, where that answers it.\n")
	case webRefused:
		b.WriteString("\n## The web\n\nThe person asked to look things up on the web, but web lookups are off here. Work from what you have, and say so if an answer needs the web.\n")
	}
}

// writeField is one field's line in the system prompt: its name, what it
// is, and what it holds now.
func writeField(b *strings.Builder, f Field, v any) {
	fmt.Fprintf(b, "- %s", f.Field)
	var about []string
	if f.Label != "" {
		about = append(about, f.Label)
	}
	if f.Type != "" {
		about = append(about, f.Type)
	}
	if len(f.Options) > 0 {
		about = append(about, "one of: "+strings.Join(f.Options, ", "))
	}
	if f.Required {
		about = append(about, "required")
	}
	if f.Secret {
		about = append(about, "secret: you see only whether it is set, and you never fill it")
	}
	if len(f.Columns) > 0 {
		var cols []string
		for _, c := range f.Columns {
			cols = append(cols, c.Field)
		}
		about = append(about, "a list of objects with "+strings.Join(cols, ", "))
	}
	if len(about) > 0 {
		fmt.Fprintf(b, " (%s)", strings.Join(about, "; "))
	}
	if f.Help != "" {
		fmt.Fprintf(b, ": %s", f.Help)
	}
	if f.Detail != "" {
		fmt.Fprintf(b, " %s", f.Detail)
	}
	now := "(empty)"
	if f.Secret {
		// The browser sends a secret as whether it is set; a value that came
		// anyway is not repeated to the model.
		if s, _ := v.(string); s != "" && s != "(empty)" {
			now = "(set)"
		}
		fmt.Fprintf(b, "\n  Now: %s\n", now)
		return
	}
	if v != nil {
		if raw, err := json.Marshal(v); err == nil && string(raw) != `""` && string(raw) != "null" && string(raw) != "[]" {
			now = string(raw)
			if len(now) > maxValue {
				now = now[:maxValue] + " [... cut]"
			}
		}
	}
	fmt.Fprintf(b, "\n  Now: %s\n", now)
}

// fieldNames are the fields a proposal may fill: the form's, secrets aside.
func (t turn) fieldNames() map[string]bool {
	names := map[string]bool{}
	for _, f := range t.req.Form {
		if !f.Secret {
			names[f.Field] = true
		}
	}
	return names
}

// stream is how the answer goes back: as JSON lines, flushed as they come
// ({progress} while it works, then the reply), to a client that asks for
// them (Accept: application/x-ndjson, as uiAssistPost does); else as one
// JSON reply at the end, as the assists that came before this answered.
type stream struct {
	mu      sync.Mutex
	w       http.ResponseWriter
	lines   bool
	started bool
}

func newStream(w http.ResponseWriter, lines bool) *stream { return &stream{w: w, lines: lines} }

// send writes one line: progress, which a plain JSON client never sees.
func (s *stream) send(v any) {
	if !s.lines {
		return
	}
	s.line(v)
}

// final is the reply.
func (s *stream) final(rp Reply) {
	if s.lines {
		s.line(rp)
		return
	}
	s.w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(s.w).Encode(rp)
}

// fail ends the turn with msg: a line, or a 502.
func (s *stream) fail(msg string) {
	if s.lines {
		s.line(Reply{Error: msg})
		return
	}
	http.Error(s.w, msg, http.StatusBadGateway)
}

func (s *stream) line(v any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.started {
		s.w.Header().Set("Content-Type", "application/x-ndjson")
		s.w.Header().Set("Cache-Control", "no-store")
		s.w.Header().Set("X-Accel-Buffering", "no")
		s.started = true
	}
	line, err := json.Marshal(v)
	if err != nil {
		return
	}
	s.w.Write(append(line, '\n'))
	if f, ok := s.w.(http.Flusher); ok {
		f.Flush()
	}
}

func hasCap(caps []Capability, c Capability) bool {
	for _, x := range caps {
		if x == c {
			return true
		}
	}
	return false
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}
