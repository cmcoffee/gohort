package mcpserver

// An action that confirms first, asked in the MCP client.
//
// An app's action can ask before it runs (AppAction.Confirm). In gohort the
// question is the confirmation card; over MCP it is MCP elicitation: during
// the tools/call the server asks the client to put a question to its person,
// and waits for the answer. The call is answered as an event stream: the
// question goes out on it, the client POSTs the answer as a JSON-RPC response
// of its own, and the call's result follows on the stream.
//
// Only at an app's own address, and only for a client that:
//   - said at initialize that it can ask (capabilities.elicitation), on a
//     key set to ask in the client (customapps.MCPKeyAsks, off by default):
//     it is then given a session, and only a session lists these actions;
//   - accepts an event stream on the call.
//
// An answer counts only on the session that was asked, from the same key,
// for that question, once. Accept runs the action as the owner's click
// would; decline, cancel, an error, no answer within questionWait, or the call
// going away refuses it, and it does not run. "Don't ask again" on an accept
// keeps a yes for that key and that action alone (customapps.RememberMCPYes),
// which the app's page and the owner's agents do not share.
//
// Sessions live in memory: after a restart a client's session is unknown,
// and it is answered as one that cannot be asked, as before this existed.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cmcoffee/gohort/apps/customapps"
	. "github.com/cmcoffee/gohort/core"
)

const (
	// askSessionIdle is how long a session lasts unused.
	askSessionIdle = 24 * time.Hour
	// askSessionsPerKey caps one key's sessions; the oldest goes first.
	askSessionsPerKey = 20
	// askKeepAlive is the spacing of the comments that keep a waiting
	// stream open through proxies.
	askKeepAlive = 25 * time.Second
	// sessionHeader carries the session on every request after initialize.
	sessionHeader = "Mcp-Session-Id"
)

// questionWait is how long a question waits for its answer; a variable so
// tests can wait less.
var questionWait = 5 * time.Minute

// keyAsks, keyRunsUnasked and keyRemember are the key's asking as customapps
// keeps it, variables so tests can stand in.
var (
	keyAsks        = customapps.MCPKeyAsks
	keyRunsUnasked = customapps.MCPRunsUnasked
	keyRemember    = customapps.RememberMCPYes
)

// askAnswer is a client's answer to a question.
type askAnswer struct {
	Action   string         `json:"action"` // accept, decline, cancel
	Content  map[string]any `json:"content"`
	Rejected string         `json:"-"` // the client answered with an error
}

// askSession is a client that can be asked: one key, one app.
type askSession struct {
	key, slug string
	started   time.Time
	seen      time.Time
	waiting   map[string]chan askAnswer // by the question's request id
}

var (
	askMu       sync.Mutex
	askSessions = map[string]*askSession{}
)

// openAskSession starts a session for key at slug and returns its id. Old
// and surplus sessions go as it does.
func openAskSession(key, slug string, now time.Time) string {
	askMu.Lock()
	defer askMu.Unlock()
	var mine []string
	for id, s := range askSessions {
		if now.Sub(s.seen) > askSessionIdle && len(s.waiting) == 0 {
			delete(askSessions, id)
			continue
		}
		if s.key == key {
			mine = append(mine, id)
		}
	}
	sort.Slice(mine, func(i, j int) bool { return askSessions[mine[i]].started.Before(askSessions[mine[j]].started) })
	for len(mine) >= askSessionsPerKey {
		delete(askSessions, mine[0])
		mine = mine[1:]
	}
	id := UUIDv4()
	askSessions[id] = &askSession{key: key, slug: slug, started: now, seen: now, waiting: map[string]chan askAnswer{}}
	return id
}

// askSessionOf is the request's session when it is key's at slug, "" when
// there is none: no header, an unknown one (a restart forgets them), or
// another key's or app's.
func askSessionOf(r *http.Request, key, slug string) string {
	id := strings.TrimSpace(r.Header.Get(sessionHeader))
	if id == "" || key == "" {
		return ""
	}
	askMu.Lock()
	defer askMu.Unlock()
	s, ok := askSessions[id]
	if !ok || s.key != key || s.slug != slug {
		return ""
	}
	s.seen = time.Now()
	return id
}

// closeAskSession ends a session the client is done with.
func closeAskSession(r *http.Request, key, slug string) bool {
	id := askSessionOf(r, key, slug)
	if id == "" {
		return false
	}
	askMu.Lock()
	defer askMu.Unlock()
	delete(askSessions, id)
	return true
}

// await registers a question on a session and returns where its answer
// arrives, and what takes it back off when the call ends.
func await(session, question string) (chan askAnswer, func()) {
	ch := make(chan askAnswer, 1)
	askMu.Lock()
	if s, ok := askSessions[session]; ok {
		s.waiting[question] = ch
	}
	askMu.Unlock()
	return ch, func() {
		askMu.Lock()
		if s, ok := askSessions[session]; ok {
			delete(s.waiting, question)
		}
		askMu.Unlock()
	}
}

// deliver hands an answer to the question it answers, once. False when no
// such question waits on that session.
func deliver(session, question string, a askAnswer) bool {
	askMu.Lock()
	defer askMu.Unlock()
	s, ok := askSessions[session]
	if !ok {
		return false
	}
	ch, ok := s.waiting[question]
	if !ok {
		return false
	}
	delete(s.waiting, question)
	ch <- a
	return true
}

// clientCanAsk reports whether initialize's params say the client can put a
// question to its person.
func clientCanAsk(params json.RawMessage) bool {
	var p struct {
		Capabilities map[string]json.RawMessage `json:"capabilities"`
	}
	if json.Unmarshal(params, &p) != nil {
		return false
	}
	raw, ok := p.Capabilities["elicitation"]
	return ok && strings.HasPrefix(strings.TrimSpace(string(raw)), "{")
}

// appMessage is anything a client POSTs: a request, a notification, or its
// answer to a question (a response: an id, a result or error, no method).
type appMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error"`
}

// isAnswer reports a client's response to a question of ours.
func (m appMessage) isAnswer() bool {
	return m.Method == "" && len(m.ID) > 0 && (len(m.Result) > 0 || m.Error != nil)
}

// handleAnswer takes a client's answer to a question: only from the key and
// session that were asked, for a question that still waits.
func (T *MCPServer) handleAnswer(w http.ResponseWriter, r *http.Request, slug string, m appMessage) {
	tok, _, refusal := T.keyApp(r, slug, "answer")
	if refusal != "" {
		http.Error(w, refusal, http.StatusForbidden)
		return
	}
	session := askSessionOf(r, tok.ID, slug)
	var question string
	if session == "" || json.Unmarshal(m.ID, &question) != nil {
		Log("[mcpserver] app %q: an answer with no session of this key, or an id no question has (owner=%s)", slug, tok.Owner)
		http.Error(w, "no question of this session waits for that answer", http.StatusNotFound)
		return
	}
	var a askAnswer
	if m.Error != nil {
		a.Rejected = m.Error.Message
		if a.Rejected == "" {
			a.Rejected = "the client could not ask"
		}
	} else if json.Unmarshal(m.Result, &a) != nil {
		a.Rejected = "the client's answer could not be read"
	}
	if !deliver(session, question, a) {
		Log("[mcpserver] app %q: an answer for a question that no longer waits (owner=%s)", slug, tok.Owner)
		http.Error(w, "no question of this session waits for that answer", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// asksFirst reports a tool whose call asks before it runs.
func asksFirst(d AgentToolDef) bool { return d.Confirmation != nil && d.Confirmation.Asks() }

// findAppToolDef is tool name of the app at slug as tok reaches it.
func findAppToolDef(tok *AccountToken, slug, name string) (customapps.OfferingApp, AgentToolDef, bool) {
	for _, a := range reachedApps(tok, slug) {
		for _, d := range a.Tools {
			if d.Tool.Name == name {
				return a, d, true
			}
		}
	}
	return customapps.OfferingApp{}, AgentToolDef{}, false
}

// askedCall is one call of an action that confirms first, asked in the
// client.
type askedCall struct {
	w       http.ResponseWriter
	r       *http.Request
	id      json.RawMessage // the call's own request id
	tok     *AccountToken
	app     customapps.OfferingApp
	def     AgentToolDef
	args    map[string]any
	session string
}

// cannotAsk is the refusal of an action that confirms first, for a client
// that cannot be asked.
func cannotAsk(name, prompt string) map[string]any {
	return toolText(fmt.Sprintf("error: %q asks to confirm before it runs (%q), and this client cannot be asked: it runs from an MCP client only on a key set to \"Ask in the client\" (the app's Settings), from a client that can put a question to you mid-call. Or use it from the app's page, or from one of your agents in gohort", name, prompt), true)
}

// run asks and, on a yes, runs the action, answering the call on an event
// stream. Without a stream to answer on, it is refused.
func (c *askedCall) run() {
	name, conf := c.def.Tool.Name, c.def.Confirmation
	if keyRunsUnasked(c.tok.ID, conf.Scope) {
		Log("[mcpserver] app %q: %s runs unasked, as this key's person said (owner=%s)", c.app.Slug, name, c.tok.Owner)
		c.reply(c.result())
		return
	}
	fl, ok := c.w.(http.Flusher)
	if !ok || !strings.Contains(c.r.Header.Get("Accept"), "text/event-stream") {
		c.reply(cannotAsk(name, conf.Prompt))
		return
	}
	question := "ask-" + UUIDv4()
	answers, done := await(c.session, question)
	defer done()
	c.w.Header().Set("Content-Type", "text/event-stream")
	c.w.Header().Set("Cache-Control", "no-cache")
	c.w.WriteHeader(http.StatusOK)
	qid, _ := json.Marshal(question)
	c.event(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(qid), "method": "elicitation/create", "params": c.question()})
	fl.Flush()
	Log("[mcpserver] app %q: asked before %s (owner=%s)", c.app.Slug, name, c.tok.Owner)

	timeout := time.NewTimer(questionWait)
	defer timeout.Stop()
	tick := time.NewTicker(askKeepAlive)
	defer tick.Stop()
	for {
		select {
		case a := <-answers:
			c.event(c.answered(a))
			fl.Flush()
			return
		case <-timeout.C:
			Log("[mcpserver] app %q: %s not run: nobody answered (owner=%s)", c.app.Slug, name, c.tok.Owner)
			c.event(c.response(toolText(fmt.Sprintf("Nobody answered within %s, so %q did not run.", shortWait(questionWait), name), true)))
			fl.Flush()
			return
		case <-c.r.Context().Done():
			Log("[mcpserver] app %q: %s not run: the call went away before an answer (owner=%s)", c.app.Slug, name, c.tok.Owner)
			return
		case <-tick.C:
			fmt.Fprint(c.w, ": keep-alive\n\n")
			fl.Flush()
		}
	}
}

// answered is the call's response to an answer: the action's result on a
// yes, a refusal otherwise.
func (c *askedCall) answered(a askAnswer) rpcResponse {
	name := c.def.Tool.Name
	switch {
	case a.Rejected != "":
		Log("[mcpserver] app %q: %s not run: the client did not ask (%s) (owner=%s)", c.app.Slug, name, a.Rejected, c.tok.Owner)
		return c.response(toolText(fmt.Sprintf("The question could not be asked (%s), so %q did not run.", a.Rejected, name), true))
	case a.Action == "decline":
		Log("[mcpserver] app %q: %s not run: declined (owner=%s)", c.app.Slug, name, c.tok.Owner)
		return c.response(toolText(fmt.Sprintf("You declined %q, so it did not run.", name), true))
	case a.Action != "accept":
		// A cancel is a question dismissed, or one the client could not show
		// (a headless run answers every question this way): nobody said no,
		// and the reply must not say they did.
		Log("[mcpserver] app %q: %s not run: %s (owner=%s)", c.app.Slug, name, a.Action, c.tok.Owner)
		return c.response(toolText(fmt.Sprintf("The question about %q was dismissed without an answer, or the client could not show it, so it did not run.", name), true))
	}
	if again, _ := a.Content["remember"].(bool); again && c.def.Confirmation.CanRemember() {
		keyRemember(c.tok.ID, c.def.Confirmation.Scope)
		Log("[mcpserver] app %q: %s runs unasked from this key from now on (owner=%s)", c.app.Slug, name, c.tok.Owner)
	}
	return c.response(c.result())
}

// result runs the action and is the call's result.
func (c *askedCall) result() map[string]any {
	text, err := c.def.Handler(c.r.Context(), c.args)
	if err != nil {
		Log("[mcpserver] app %q: tools/call %s error (owner=%s): %v", c.app.Slug, c.def.Tool.Name, c.tok.Owner, err)
		return toolText("error: "+err.Error(), true)
	}
	Log("[mcpserver] app %q: tools/call %s ok (owner=%s, %d chars)", c.app.Slug, c.def.Tool.Name, c.tok.Owner, len(text))
	return toolResult(text, nil)
}

// question is the elicitation's params: the app's own question over what the
// call would do, with "Don't ask again" when the action may be remembered.
func (c *askedCall) question() map[string]any {
	conf := c.def.Confirmation
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %s\n\n%s", c.app.Name, c.def.Tool.Name, strings.TrimSpace(conf.Prompt))
	if len(c.args) > 0 {
		keys := make([]string, 0, len(c.args))
		for k := range c.args {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteString("\n\nWith:")
		for _, k := range keys {
			fmt.Fprintf(&b, "\n- %s: %s", k, clipValue(fmt.Sprint(c.args[k]), 300))
		}
	}
	props := map[string]any{}
	if conf.CanRemember() && conf.Scope != "" {
		props["remember"] = map[string]any{
			"type":        "boolean",
			"title":       "Don't ask again",
			"description": fmt.Sprintf("Run %s from this key without asking from now on. The app's Settings can make it ask again.", c.def.Tool.Name),
			"default":     false,
		}
	}
	return map[string]any{
		"message":         b.String(),
		"requestedSchema": map[string]any{"type": "object", "properties": props},
	}
}

// response is the call's JSON-RPC response carrying result.
func (c *askedCall) response(result map[string]any) rpcResponse {
	return rpcResponse{JSONRPC: "2.0", ID: c.id, Result: result}
}

// reply answers the call as one JSON body, before any stream was opened.
func (c *askedCall) reply(result map[string]any) {
	c.w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(c.w).Encode(c.response(result))
}

// event writes msg as one event on the call's stream.
func (c *askedCall) event(msg any) {
	b, _ := json.Marshal(msg)
	fmt.Fprintf(c.w, "event: message\ndata: %s\n\n", b)
}

// clipValue cuts s to n runes, marking the cut.
func clipValue(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "..."
	}
	return s
}

// shortWait is a wait in words.
func shortWait(d time.Duration) string {
	if m := int(d.Minutes()); m >= 1 && d%time.Minute == 0 {
		if m == 1 {
			return "a minute"
		}
		return fmt.Sprintf("%d minutes", m)
	}
	return d.String()
}
