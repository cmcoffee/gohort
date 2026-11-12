package orchestrate

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// The gate judges a voice note by what was said, as it judges typed text. It
// used to see a voice note as an empty message, and every wake rule blocked
// it, so an agent in a gated room could not be spoken to at all.
func TestTheGateHearsAVoiceNote(t *testing.T) {
	df := newDispatchFixture(t, FakeTurn{Content: `{"answer":"YES","reason":"asks about the weather"}`, Repeat: true})
	prevRoot := RootDB
	RootDB = df.app.DB
	t.Cleanup(func() { RootDB = prevRoot })
	SaveChannel(RootDB, Channel{ID: "c1", Owner: "u", Service: "imessage", Address: "chat-1", AgentID: df.agent.ID,
		Gatekeeper: "Answer only questions about the weather."})

	status := http.StatusOK
	stt := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		w.Write([]byte("what's the weather tomorrow"))
	}))
	defer stt.Close()
	transcribeAt(t, stt.URL)

	judged := func(in ChannelInbound) string {
		t.Helper()
		before := df.llm.Calls()
		if !df.app.channelGatekeeperAllow(t.Context(), in) {
			t.Fatal("the gate answered YES; the message should be let through")
		}
		if df.llm.Calls() != before+1 {
			t.Fatalf("the gate should have asked the model once, asked %d times", df.llm.Calls()-before)
		}
		var b strings.Builder
		for _, m := range df.llm.Sent(before) {
			b.WriteString(m.Content)
		}
		return b.String()
	}
	in := ChannelInbound{Owner: "u", AgentID: df.agent.ID, ChatID: "chat-1", SenderName: "Alice", Handle: "+15550100",
		Audios: []string{base64.StdEncoding.EncodeToString([]byte("not really an m4a"))}}

	if got := judged(in); !strings.Contains(got, "[voice note: what's the weather tomorrow]") {
		t.Errorf("the gate should judge the words of the voice note: %s", got)
	}

	status = http.StatusInternalServerError
	if got := judged(in); !strings.Contains(got, "[voice note that could not be transcribed]") {
		t.Errorf("an unheard voice note should still read as one: %s", got)
	}

	video := ChannelInbound{Owner: "u", AgentID: df.agent.ID, ChatID: "chat-1", SenderName: "Alice", Handle: "+15550100",
		Videos: []string{base64.StdEncoding.EncodeToString([]byte("clip"))}}
	if got := judged(video); !strings.Contains(got, "[video, no text: 1 video(s)]") {
		t.Errorf("a video with no text should read as a video, not as nothing: %s", got)
	}
}

// A message that addresses the agent by name is let through without a model
// when the wake rules wake on its name: a judge once read "Pip, is it
// still running ?" as not addressed to Pip and dropped it. Rules that do
// not mention names still decide, told that the message names the agent.
func TestAMessageByNameIsNeverDropped(t *testing.T) {
	df := newDispatchFixture(t, FakeTurn{Content: `{"answer":"NO","reason":"not directed at you"}`, Repeat: true})
	prevRoot := RootDB
	RootDB = df.app.DB
	t.Cleanup(func() { RootDB = prevRoot })
	SaveChannel(RootDB, Channel{ID: "c1", Owner: "u", Service: "imessage", Address: "chat-1", AgentID: df.agent.ID,
		Gatekeeper: "1. Respond when you are called by name."})
	in := ChannelInbound{Owner: "u", AgentID: df.agent.ID, ChatID: "chat-1", SenderName: "Alice", Handle: "+15550100",
		Text: "clerk, is it still running ?"}
	before := df.llm.Calls()
	if !df.app.channelGatekeeperAllow(t.Context(), in) || df.llm.Calls() != before {
		t.Errorf("addressed by name under a by-name rule: allowed=%v after %d model calls, want allowed with none",
			df.app.channelGatekeeperAllow(t.Context(), in), df.llm.Calls()-before)
	}

	SaveChannel(RootDB, Channel{ID: "c1", Owner: "u", Service: "imessage", Address: "chat-1", AgentID: df.agent.ID,
		Gatekeeper: "Answer only questions about the weather."})
	before = df.llm.Calls()
	if df.app.channelGatekeeperAllow(t.Context(), in) {
		t.Error("rules that never mention names keep their say: the model said NO")
	}
	if df.llm.Calls() != before+1 {
		t.Fatalf("the model was asked %d times, want once", df.llm.Calls()-before)
	}
	var sent strings.Builder
	for _, m := range df.llm.Sent(before) {
		sent.WriteString(m.Content)
	}
	if !strings.Contains(sent.String(), "addresses you by name") {
		t.Error("the model was not told the message names the agent")
	}

	// A name rule that also says who it is for is not decided in code: Alice
	// naming the agent under a rule for Henry is the model's to weigh.
	SaveChannel(RootDB, Channel{ID: "c1", Owner: "u", Service: "imessage", Address: "chat-1", AgentID: df.agent.ID,
		Gatekeeper: "WAKE when the incoming message from Henry mentions or addresses the agent by name."})
	before = df.llm.Calls()
	if df.app.channelGatekeeperAllow(t.Context(), in) {
		t.Error("a rule for Henry let Alice through on the name: the model said NO")
	}
	if df.llm.Calls() != before+1 {
		t.Errorf("under a rule for Henry the model was asked %d times, want once", df.llm.Calls()-before)
	}
}

// What counts as addressing the agent by name, and what is only mentioning it.
func TestAddressedByName(t *testing.T) {
	for text, want := range map[string]bool{
		"Pip, is it still running ?":    true,
		"pip can you make a picture":    true,
		"hey Pip, quick one":            true,
		"ok, Pip: try again":            true,
		"@pip what's next":              true,
		"is it done yet, Pip?":          true,
		"I told Pip about it yesterday": false,
		"I was just talking to Pip":     false,
		"Pipping around again":          false,
		"what do you think":             false,
	} {
		if got := addressesByName(text, "Pip"); got != want {
			t.Errorf("%q: addressed=%v, want %v", text, got, want)
		}
	}
	for rules, want := range map[string]bool{
		"Answer only questions about the weather.":           false,
		"Respond when called BY NAME":                        true,
		"2. When someone mentions you":                       true,
		"Skip messages that mention politics.":               false,
		"Is a continuation of a conversation about the game": false,
		DefaultDMGatekeeperRule:                              true,
		gateTestRules:                                        true,
		// A rule with a condition of its own is the model's to weigh.
		"WAKE when the incoming message from Henry mentions or addresses the agent by name, including common nicknames or obvious typos of that name.": false,
		"WAKE only when Henry calls you directly by name":                  false,
		"Respond when you are called by name in the morning":               false,
		"1. Respond when called by name and the message is about the game": false,
	} {
		if got := rulesWakeOnName(rules); got != want {
			t.Errorf("rules %q wake on a name=%v, want %v", rules, got, want)
		}
	}
}

// The gate asks for its reason before its answer, so the model reads the
// rules before it commits; and a reply that will not parse is read for its
// answer, not for any "yes" among the per-rule lines that now come first.
func TestTheGateReasonsBeforeItAnswers(t *testing.T) {
	r, a := strings.Index(gatekeeperSysPrompt, `"reason"`), strings.Index(gatekeeperSysPrompt, `"answer"`)
	if r < 0 || a < 0 || r > a {
		t.Errorf("the format asks for the answer before the reason:\n%s", gatekeeperSysPrompt)
	}
	for reply, want := range map[string]bool{
		`{"reason": "rule 1: yes, named. rule 2: no", "answer": "NO"`:        false,
		`{"reason": "rule 1: no. rule 2: yes, a follow-up", "answer": "YES"`: true,
		"rule 1: yes, it names you. So the answer is NO.":                    false,
		"rule 1: no. rule 2: yes. Answer: YES":                               true,
		"I cannot tell.":                                                     false,
	} {
		if got := rawGateAnswer(reply); got != want {
			t.Errorf("%q read as allow=%v, want %v", reply, got, want)
		}
	}
}

// A reply the gate cannot parse is decided by its answer: per-rule "yes"
// lines in the reason do not let the message through.
func TestAnUnreadableGateReplyGoesByItsAnswer(t *testing.T) {
	df := newDispatchFixture(t, FakeTurn{Content: "rule 1: yes, it is about the weather. rule 2: no. Answer: NO", Repeat: true})
	prevRoot := RootDB
	RootDB = df.app.DB
	t.Cleanup(func() { RootDB = prevRoot })
	SaveChannel(RootDB, Channel{ID: "c1", Owner: "u", Service: "imessage", Address: "chat-1", AgentID: df.agent.ID,
		Gatekeeper: "Answer only questions about the weather."})
	in := ChannelInbound{Owner: "u", AgentID: df.agent.ID, ChatID: "chat-1", SenderName: "Alice", Handle: "+15550100", Text: "nice day"}
	if df.app.channelGatekeeperAllow(t.Context(), in) {
		t.Error("an unparsed reply that answers NO let the message through")
	}
}

// With Services > System One on, the gate asks it first: a sure "take it up"
// lets the message through, and so does nothing else, without the full
// answer; an unsure one goes on to the full answer. Off, System One is not
// asked at all.
func TestTheGateAsksSystemOneFirst(t *testing.T) {
	df := newDispatchFixture(t, FakeTurn{Content: `{"reason": "rule 1: no", "answer": "NO"}`, Repeat: true})
	prevRoot := RootDB
	RootDB = df.app.DB
	prevGate := JudgeMapGate
	// The gate's System One form is mapped (its cases came out right).
	JudgeMapGate = func(key, tier string) bool { return key == gatekeeperRoute && tier == MapSystemOne }
	t.Cleanup(func() { RootDB = prevRoot; SetSharedJudgeLLM(nil, false, "", ""); JudgeMapGate = prevGate })
	SaveChannel(RootDB, Channel{ID: "c1", Owner: "u", Service: "imessage", Address: "chat-1", AgentID: df.agent.ID,
		Gatekeeper: "Answer only questions about the weather."})
	take := 0.95
	asked := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked++
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"answers":{"d":{"probabilities":{"o00":%g,"o01":%g}}},"diagnostics":{"d":{"label_mass":1}}}`, take, 1-take)
	}))
	defer srv.Close()
	judge, err := NewLLMFromConfig(LLMProviderConfig{Provider: "vllm", Endpoint: srv.URL + "/v1", SeparateServer: true})
	if err != nil {
		t.Fatal(err)
	}
	SetSharedJudgeLLM(judge, false, "judge", "")
	in := ChannelInbound{Owner: "u", AgentID: df.agent.ID, ChatID: "chat-1", SenderName: "Alice", Handle: "+15550100", Text: "will it rain tomorrow?"}

	if df.app.channelGatekeeperAllow(t.Context(), in); asked != 0 {
		t.Fatalf("System One was asked while off (%d asks)", asked)
	}
	RootDB.Set(systemOneTable, "provider", "judge")
	before := df.llm.Calls()
	if !df.app.channelGatekeeperAllow(t.Context(), in) || df.llm.Calls() != before || asked != 2 {
		t.Errorf("a sure take-it-up: let through with %d full answers and %d System One asks, want 0 and 2", df.llm.Calls()-before, asked)
	}
	take = 0.55 // a lean, not a decision
	before = df.llm.Calls()
	if df.app.channelGatekeeperAllow(t.Context(), in) || df.llm.Calls() != before+1 {
		t.Errorf("an unsure System One did not go on to the full answer (%d full answers)", df.llm.Calls()-before)
	}
}
