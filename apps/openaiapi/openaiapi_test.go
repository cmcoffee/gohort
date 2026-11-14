package openaiapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/agenthost"
	"github.com/cmcoffee/gohort/core/modelaccess"
	"github.com/cmcoffee/snugforge/kvlite"
)

// TestTextContentShapes: clients send message content either as a plain string
// or as an array of typed parts. Mishandling the array form yields an empty
// prompt and a confident answer to nothing.
func TestTextContentShapes(t *testing.T) {
	if got := textContent("hello"); got != "hello" {
		t.Errorf("string form: %q", got)
	}
	var parts any
	_ = json.Unmarshal([]byte(`[{"type":"text","text":"who is "},{"type":"text","text":"on call?"}]`), &parts)
	if got := textContent(parts); got != "who is on call?" {
		t.Errorf("array form: %q", got)
	}
	// Non-text parts are skipped, not stringified into the prompt.
	var mixed any
	_ = json.Unmarshal([]byte(`[{"type":"image_url","image_url":{"url":"x"}},{"type":"text","text":"hi"}]`), &mixed)
	if got := textContent(mixed); got != "hi" {
		t.Errorf("mixed form: %q", got)
	}
	if got := textContent(nil); got != "" {
		t.Errorf("nil: %q", got)
	}
	if got := textContent(42); got != "" {
		t.Errorf("unexpected type should be empty, got %q", got)
	}
}

// TestSessionKeyPrecedence: a caller's own session or call id is its thread;
// one that names none gets the prefix its conversations share, so it is not
// held in one thread forever.
func TestSessionKeyPrecedence(t *testing.T) {
	req := chatReq{User: "u1"}
	req.Call.ID = "call-abc"
	check := func(r *http.Request, want string, wantRolling bool, what string) {
		t.Helper()
		if got, rolling := sessionKey(r, req); got != want || rolling != wantRolling {
			t.Errorf("%s: got %q rolling=%v, want %q rolling=%v", what, got, rolling, want, wantRolling)
		}
	}
	check(newReq(map[string]string{"X-Session-Id": "hdr-1"}), "ext:hdr-1", false, "header wins")
	check(newReq(nil), "ext:call-abc", false, "call id next")
	req.Call.ID = ""
	check(newReq(nil), "ext:conv:u1:", true, "user field names whose conversations")
	req.User = ""
	check(newReq(nil), "ext:conv:", true, "fallback")
}

// TestSentencesSplitsForTTS: the agent path has no token stream, so the reply
// is chunked into speakable units — a consumer can start on sentence one.
func TestSentencesSplitsForTTS(t *testing.T) {
	got := sentences("Two things. First, the deploy is green! Second?")
	if len(got) != 3 {
		t.Fatalf("expected 3 chunks, got %d: %q", len(got), got)
	}
	if strings.Join(got, "") != "Two things. First, the deploy is green! Second?" {
		t.Errorf("chunks must reassemble to the original: %q", got)
	}
	// No terminator: one chunk, nothing lost.
	if got := sentences("no terminator here"); len(got) != 1 || got[0] != "no terminator here" {
		t.Errorf("unterminated: %q", got)
	}
	if got := sentences(""); len(got) != 1 || got[0] != "" {
		t.Errorf("empty input should yield the input, got %q", got)
	}
	// Trailing text after the last terminator still ships.
	got = sentences("Done. And one more thing")
	if len(got) != 2 || !strings.Contains(got[1], "one more thing") {
		t.Errorf("trailing fragment dropped: %q", got)
	}
}

// TestCompletionEnvelopeShape pins the non-streaming response to the shape an
// OpenAI client library parses.
func TestCompletionEnvelopeShape(t *testing.T) {
	env := completionEnvelope("worker", "hi there", nil)
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back struct {
		Object  string `json:"object"`
		Choices []struct {
			Message struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage *struct{} `json:"usage"`
	}
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Object != "chat.completion" || len(back.Choices) != 1 {
		t.Fatalf("bad envelope: %s", b)
	}
	if back.Choices[0].Message.Role != "assistant" || back.Choices[0].Message.Content != "hi there" {
		t.Errorf("message: %+v", back.Choices[0].Message)
	}
	if back.Choices[0].FinishReason != "stop" {
		t.Errorf("finish_reason: %q", back.Choices[0].FinishReason)
	}
	// usage is omitted when no Response carried token counts.
	if back.Usage != nil {
		t.Errorf("usage should be absent without a response")
	}
}

// newReq builds a bare request carrying the given headers.
func newReq(headers map[string]string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	return r
}

// TestTurnInputPicksLastUserAndFirstSystem: a voice platform resends the whole
// transcript each turn. The LAST user message is this turn's input — taking the
// first would answer the opening line over and over.
func TestTurnInputPicksLastUserAndFirstSystem(t *testing.T) {
	var req chatReq
	body := `{"messages":[
		{"role":"system","content":"you are terse"},
		{"role":"user","content":"first thing"},
		{"role":"assistant","content":"ok"},
		{"role":"user","content":"second thing"},
		{"role":"system","content":"ignored second system"}
	]}`
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	input, system := turnInput(req)
	if input != "second thing" {
		t.Errorf("input = %q, want the LAST user message", input)
	}
	if system != "you are terse" {
		t.Errorf("system = %q, want the FIRST system message", system)
	}
}

// TestTurnInputFallsBackWhenNoUser: some clients (Vapi's validation ping) send a
// single message that isn't role "user". Rejecting it with "no user message"
// turned a request the passthrough tier answered into an HTTP 400 the moment a
// model was prefixed with channel:/agent: to scope a key. The agent path must be
// as tolerant as passthrough: answer the last non-empty message of any role.
func TestTurnInputFallsBackWhenNoUser(t *testing.T) {
	// Lone system message (the msgs=1 shape seen live). It becomes the input,
	// and is NOT also fed back as the system prompt (which would double it).
	sysOnly := chatReq{Messages: []struct {
		Role    string `json:"role"`
		Content any    `json:"content"`
	}{{Role: "system", Content: "You are a helpful assistant."}}}
	if in, sys := turnInput(sysOnly); in != "You are a helpful assistant." || sys != "" {
		t.Errorf("lone system: input=%q system=%q, want the system text as input and empty system", in, sys)
	}
	// An assistant-only tail (no user turn) still yields something to answer.
	asstTail := chatReq{Messages: []struct {
		Role    string `json:"role"`
		Content any    `json:"content"`
	}{{Role: "system", Content: "be terse"}, {Role: "assistant", Content: "How can I help?"}}}
	if in, sys := turnInput(asstTail); in != "How can I help?" || sys != "be terse" {
		t.Errorf("assistant tail: input=%q system=%q", in, sys)
	}
	// A real user turn still wins over the fallback.
	withUser := chatReq{Messages: []struct {
		Role    string `json:"role"`
		Content any    `json:"content"`
	}{{Role: "system", Content: "be terse"}, {Role: "user", Content: "hello"}, {Role: "assistant", Content: "hi"}}}
	if in, sys := turnInput(withUser); in != "hello" || sys != "be terse" {
		t.Errorf("with user: input=%q system=%q, want the user turn to win", in, sys)
	}
	// Truly empty message set → no input (the one case that still 400s).
	if in, _ := turnInput(chatReq{}); in != "" {
		t.Errorf("empty: input=%q, want empty", in)
	}
}

// TestCallerNamePrecedence: the speaker is labeled in the thread, so a group
// transcript reads as who-said-what rather than attributing a call to the room.
func TestCallerNamePrecedence(t *testing.T) {
	req := chatReq{User: "+15551234567"}
	if got := callerName(newReq(map[string]string{"X-Caller-Name": "Alice"}), req); got != "Alice" {
		t.Errorf("header wins: %q", got)
	}
	if got := callerName(newReq(nil), req); got != "+15551234567" {
		t.Errorf("user field next: %q", got)
	}
	if got := callerName(newReq(nil), chatReq{}); got != "API client" {
		t.Errorf("fallback should be honest, got %q", got)
	}
	// Any other client is named by its key, as an MCP ask is: the name its
	// owner gave the key, after a caller a voice platform named.
	prevRoot, prevAuth := RootDB, AuthDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	AuthDB = func() Database { return RootDB }
	t.Cleanup(func() { RootDB, AuthDB = prevRoot, prevAuth })
	key := MintAccountTokenScoped("alice", "Open WebUI", &TokenScope{Features: []string{OpenAIFeatureKey}})
	if got := callerName(newReq(map[string]string{"X-API-Key": key.Token}), chatReq{}); got != "Open WebUI" {
		t.Errorf("a client that names nobody is its key's name: %q", got)
	}
	if got := callerName(newReq(map[string]string{"X-API-Key": key.Token, "X-Caller-Name": "Alice"}), chatReq{}); got != "Alice" {
		t.Errorf("a named caller still wins over the key: %q", got)
	}
}

// TestIsTierName pins which model strings mean "raw model" rather than "resolve
// this to a conversation". Getting this wrong sends a caller who named a chat
// to the bare worker tier — which answers plausibly with no persona, tools,
// memory or thread, and is therefore the hardest failure to notice.
func TestIsTierName(t *testing.T) {
	for _, m := range []string{"worker", "lead", "WORKER", " Lead ", "gohort-worker", "default"} {
		if !isTierName(m) {
			t.Errorf("%q should name a tier", m)
		}
	}
	for _, m := range []string{"any;+;chat872212368359368118", "seed-chat", "Family", "", "gpt-4"} {
		if isTierName(m) {
			t.Errorf("%q should NOT name a tier", m)
		}
	}
}

func TestCanonicalTier(t *testing.T) {
	cases := map[string]string{
		"worker": "worker", "gohort-worker": "worker", "gohort": "worker",
		"default": "worker", "": "worker",
		"lead": "lead", "GOHORT-LEAD": "lead", " Lead ": "lead",
	}
	for in, want := range cases {
		if got := canonicalTier(in); got != want {
			t.Errorf("canonicalTier(%q)=%q want %q", in, got, want)
		}
	}
}

// gateTarget is the per-key allow-list check (gate 3). A specific agent must be
// on the key's Targets to pass; a legacy or nil token grandfathers through.
func TestGateTarget(t *testing.T) {
	scoped := &AccountToken{ID: "k1", Scope: &TokenScope{Targets: []string{"agent:abc", "worker"}}}
	legacy := &AccountToken{ID: "k2"} // nil Scope

	check := func(tok *AccountToken, canon string) int {
		w := httptest.NewRecorder()
		gateTarget(w, "alice", tok, canon)
		return w.Code
	}
	if c := check(scoped, "agent:abc"); c != 200 {
		t.Errorf("listed agent should pass, got %d", c)
	}
	if c := check(scoped, "agent:xyz"); c != http.StatusForbidden {
		t.Errorf("unlisted agent must be 403, got %d", c)
	}
	if c := check(scoped, "lead"); c != http.StatusForbidden {
		t.Errorf("unlisted tier must be 403, got %d", c)
	}
	if c := check(legacy, "agent:anything"); c != 200 {
		t.Errorf("legacy key grandfathers through, got %d", c)
	}
	if c := check(nil, "agent:anything"); c != 200 {
		t.Errorf("nil token (non-account auth) skips per-key gate, got %d", c)
	}
}

// reasoning_effort: unset follows the deployment's settings, none is no
// thinking, a known level is that level, and a value nobody defined is refused
// rather than guessed.
func TestReasoningEffortLevels(t *testing.T) {
	for in, want := range map[string]string{"": "", "none": effortOff, "minimal": effortOff, "low": "low", "Medium": "medium", "high": "high", "xhigh": "high"} {
		got, ok := reasoningEffort(in)
		if !ok || got != want {
			t.Errorf("reasoningEffort(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	if _, ok := reasoningEffort("max"); ok {
		t.Error("an unknown level should be refused")
	}
}

// A passthrough call thinks as its tier is set to unless the caller chose:
// asked nothing, it names no thinking, so the tier's default applies.
func TestPassthroughThinksAsItsTierIsSet(t *testing.T) {
	ask := func(effort string) ChatConfig {
		llm := &FakeLLM{Turns: []FakeTurn{{Content: "hi"}}}
		api := &OpenAIAPI{}
		api.LLM = llm
		req := chatReq{Model: "worker", ReasoningEffort: effort}
		req.Messages = append(req.Messages, struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		}{Role: "user", Content: "hello"})
		w := httptest.NewRecorder()
		api.servePassthrough(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil), "u", req)
		if w.Code != http.StatusOK || llm.Calls() != 1 {
			t.Fatalf("passthrough did not answer: %d %s", w.Code, w.Body.String())
		}
		return llm.Config(0)
	}
	// The worker call turns thinking on when a caller names none, and the
	// tier's administrator default then sets its level: so nothing forced
	// off, and no level forced.
	if cfg := ask(""); (cfg.Think != nil && !*cfg.Think) || cfg.Effort != "" {
		t.Errorf("a caller that asked for nothing should get the tier's setting, got think=%v effort=%q", cfg.Think != nil && *cfg.Think, cfg.Effort)
	}
	if cfg := ask(effortOff); cfg.Think == nil || *cfg.Think {
		t.Error("a caller that asked for none should get no thinking")
	}
	if cfg := ask("medium"); cfg.Think == nil || !*cfg.Think || cfg.Effort != "medium" {
		t.Errorf("a caller that asked for medium should get it, got %+v", cfg)
	}
}

// An agent turn with no reasoning_effort thinks as the agent is set to, and the
// agents it hands work to as they are, as in the app: it used to be off down
// the whole chain, which made an agent reached here weaker than the same agent
// in the app. One asked for none runs with thinking off and marks its context,
// so the agents below stay quiet too; one with a level thinks at it and leaves
// the agents below to their own.
func TestAnAgentTurnThinksAsTheAgentIsSet(t *testing.T) {
	var run agenthost.Run
	ctx := agentTurn{}.thinking(context.Background(), &run)
	if run.Think != nil || run.Effort != "" || ctx != context.Background() {
		t.Errorf("no reasoning_effort should leave the agents to their own settings, got think=%v effort=%q marked=%v", run.Think, run.Effort, ctx != context.Background())
	}
	run = agenthost.Run{}
	ctx = agentTurn{effort: effortOff}.thinking(context.Background(), &run)
	if run.Think == nil || *run.Think || ctx == context.Background() {
		t.Errorf("none should mean no thinking on the chain, got think=%v marked=%v", run.Think != nil && *run.Think, ctx != context.Background())
	}
	run = agenthost.Run{}
	ctx = agentTurn{effort: "high"}.thinking(context.Background(), &run)
	if run.Think == nil || !*run.Think || run.Effort != "high" || ctx != context.Background() {
		t.Errorf("a caller that asked for high should get it, unmarked, got %+v", run)
	}
}

// /v1/models offers the agents MCP's list_agents offers for the same key: one
// list, with the key's explicit grants. It used to list every exposed agent by
// a rule of its own, which showed an app's hidden agent by its internal id and
// left out an agent the key was granted but that was not exposed, though the
// resolver accepted it.
func TestModelsListTheAgentsMCPLists(t *testing.T) {
	saved := ListExternalReachableAgentsFn
	t.Cleanup(func() { ListExternalReachableAgentsFn = saved })
	var gotOwner string
	var gotGrant func(string) bool
	ListExternalReachableAgentsFn = func(db Database, owner string, granted func(string) bool) []ExternalAgentInfo {
		gotOwner, gotGrant = owner, granted
		return []ExternalAgentInfo{{ID: "wren-1", Name: "Wren"}}
	}

	token := &AccountToken{Scope: &TokenScope{Targets: []string{"agent:granted-1"}}}
	got := modelAgents("alice", token)
	if len(got) != 1 || got[0].ID != "wren-1" || gotOwner != "alice" {
		t.Fatalf("modelAgents = %+v for %q, want the hook's list for alice", got, gotOwner)
	}
	if gotGrant == nil || !gotGrant("agent:granted-1") || gotGrant("agent:other") {
		t.Error("the key's explicit grants did not reach the list")
	}

	ListExternalReachableAgentsFn = nil
	if got := modelAgents("alice", token); got != nil {
		t.Errorf("no agent host, no agents: %+v", got)
	}
}

// outsideHost records the run an agent turn asks for.
type outsideHost struct {
	agenthost.Host
	runs   []agenthost.Run
	thread string // whose store RecentThread was asked to look in
	err    error
}

func (h *outsideHost) RunAgentSyncContinuingRich(ctx context.Context, run agenthost.Run) (agenthost.Result, error) {
	h.runs = append(h.runs, run)
	return agenthost.Result{Text: "hello"}, h.err
}

func (h *outsideHost) RecentThread(owner, agentID, prefix string, idle time.Duration) string {
	h.thread = owner
	return ""
}

// A key for people outside gohort runs each call in the named person's own
// store, refuses a call that names nobody, and says plainly when the agent is
// not set to serve them. An ordinary key's turn still runs as its owner.
func TestAnOutsideKeyRunsEachPersonInTheirOwnStore(t *testing.T) {
	prevDB, prevResolve := RootDB, ResolveExternalAgentFn
	RootDB = &DBase{Store: kvlite.MemStore()}
	ResolveExternalAgentFn = func(db Database, owner, key string, granted func(string) bool) (string, bool) { return "a1", true }
	host := &outsideHost{}
	prevHost := agenthost.Get()
	agenthost.Register(host)
	t.Cleanup(func() { RootDB, ResolveExternalAgentFn = prevDB, prevResolve; agenthost.Register(prevHost) })

	outside := MintAccountTokenScoped("alice", "portal", &TokenScope{Outside: true, Features: []string{"openai"}, Targets: []string{"agent:a1"}})
	plain := MintAccountTokenScoped("alice", "voice", &TokenScope{Features: []string{"openai"}, Targets: []string{"agent:a1"}})

	call := func(key string, headers map[string]string, user string) *httptest.ResponseRecorder {
		r := newReq(headers)
		r.Header.Set("X-API-Key", key)
		req := chatReq{Model: "agent:a1", User: user}
		req.Messages = append(req.Messages, struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		}{"user", "hi"})
		w := httptest.NewRecorder()
		(&OpenAIAPI{}).serveAgent(w, r, "alice", "a1", req)
		return w
	}

	if w := call(outside.Token, nil, ""); w.Code != http.StatusBadRequest || len(host.runs) != 0 {
		t.Fatalf("a call naming nobody: %d with %d run(s); want 400 and no run", w.Code, len(host.runs))
	}
	if w := call(outside.Token, map[string]string{"X-End-User": "cust:7"}, ""); w.Code != http.StatusBadRequest || len(host.runs) != 0 {
		t.Fatalf("an id that cannot name a store: %d with %d run(s); want 400 and no run", w.Code, len(host.runs))
	}

	want := agenthost.OutsideScope(outside.ID, "cust-7")
	if w := call(outside.Token, map[string]string{"X-End-User": "cust-7"}, "ignored-when-the-header-names-them"); w.Code != http.StatusOK {
		t.Fatalf("a named call: %d %s", w.Code, w.Body.String())
	}
	run := host.runs[len(host.runs)-1]
	if run.RuntimeUser != want || run.AgentOwner != "alice" || run.MessageSender != "cust-7" {
		t.Errorf("ran as %q for %q, sender %q; want %q for alice, sender cust-7", run.RuntimeUser, run.AgentOwner, run.MessageSender, want)
	}
	if host.thread != want {
		t.Errorf("the conversation was looked up in %q's store, want the person's", host.thread)
	}
	// The user field names them when the header does not.
	call(outside.Token, nil, "cust-8")
	if got := host.runs[len(host.runs)-1].RuntimeUser; got != agenthost.OutsideScope(outside.ID, "cust-8") {
		t.Errorf("the user field did not name the person: ran as %q", got)
	}

	host.err = agenthost.ErrNotForOutsideAudience
	if w := call(outside.Token, map[string]string{"X-End-User": "cust-7"}, ""); w.Code != http.StatusForbidden {
		t.Errorf("an agent not set to serve people outside: %d, want 403", w.Code)
	}
	host.err = nil

	call(plain.Token, map[string]string{"X-End-User": "cust-7"}, "")
	if got := host.runs[len(host.runs)-1].RuntimeUser; got != "alice" {
		t.Errorf("an ordinary key's turn ran as %q, want its owner", got)
	}
}

// The raw tiers are Model access: refused until an administrator grants it,
// listed only then, and every call counts against its per-user limits.
func TestTheRawTiersAreModelAccess(t *testing.T) {
	prevRoot, prevAuth := RootDB, AuthDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	AuthDB = func() Database { return RootDB }
	t.Cleanup(func() { RootDB, AuthDB = prevRoot, prevAuth })
	RootDB.Set(AuthTable, "user:alice", AuthUser{Username: "alice"})
	SetFeatureAllowedUsers(RootDB, OpenAIFeatureKey, []string{"alice"}) // the Model API itself, which is Closed
	tok := MintAccountTokenScoped("alice", "client", &TokenScope{Features: []string{OpenAIFeatureKey}, Targets: []string{"worker"}})
	api := &OpenAIAPI{}
	api.LLM = &FakeLLM{Turns: []FakeTurn{{Content: "hi", Repeat: true}}}

	chat := func() int {
		r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"worker","messages":[{"role":"user","content":"hello"}]}`))
		r.Header.Set("X-API-Key", tok.Token)
		w := httptest.NewRecorder()
		api.handleChatCompletions(w, r)
		return w.Code
	}
	listsWorker := func() bool {
		r := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		r.Header.Set("X-API-Key", tok.Token)
		w := httptest.NewRecorder()
		api.handleModels(w, r)
		return strings.Contains(w.Body.String(), `"id":"worker"`)
	}

	if code := chat(); code != http.StatusForbidden {
		t.Errorf("a raw tier without Model access: %d, want 403", code)
	}
	if listsWorker() {
		t.Error("the model list offered a raw tier without Model access")
	}
	SetFeatureAllowedUsers(RootDB, ModelAccessFeature, []string{"alice"})
	if err := modelaccess.Save(modelaccess.Limits{PerMinute: 1, AtOnce: 2}); err != nil {
		t.Fatal(err)
	}
	if code := chat(); code != http.StatusOK {
		t.Errorf("a raw tier with Model access: %d, want 200", code)
	}
	if !listsWorker() {
		t.Error("the model list left out a raw tier the key may reach")
	}
	if code := chat(); code != http.StatusTooManyRequests {
		t.Errorf("past the per-minute limit: %d, want 429", code)
	}
}
