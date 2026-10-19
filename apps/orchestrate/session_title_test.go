package orchestrate

// A conversation that opened with "Hello" was named "Casual Greeting" from
// that greeting alone and kept it. A title now waits for an exchange with
// something in it, is written once more from the first three, and never
// replaces a name the user gave.

import (
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// asked builds a session that has had one exchange per message.
func asked(title, auto string, msgs ...string) ChatSession {
	s := ChatSession{ID: UUIDv4(), AgentID: "a1", Title: title, AutoTitle: auto}
	for _, m := range msgs {
		s.Messages = append(s.Messages, ChatMessage{Role: "user", Content: m}, ChatMessage{Role: "assistant", Content: "reply to " + m})
	}
	return s
}

func TestAGreetingWaitsForSomethingToNameBy(t *testing.T) {
	song := "make me a parody song about Mondays"
	for _, c := range []struct {
		name string
		s    ChatSession
		due  bool
	}{
		{"a bare hello waits", asked("Hello", "Hello", "Hello"), false},
		{"the exchange after it names it", asked("Hello", "Hello", "Hello", song), true},
		{"the third exchange names it again", asked("x", "x", "Hello", song, "harder mix"), true},
		{"and then it is left alone", asked("x", "x", "Hello", song, "harder mix", "thanks"), false},
		{"a first message with a subject names it", asked(song, song, song), true},
		{"which the second does not redo", asked("x", "x", song, "harder mix"), false},
		{"but the third does", asked("x", "x", song, "harder mix", "thanks"), true},
		{"a name the user gave is kept", asked("My Monday songs", "x", "Hello", song, "harder mix"), false},
		{"a session from before AutoTitle keeps its title", asked("Old title", "", song, "a", "b"), false},
	} {
		if got := titleDue(c.s); got != c.due {
			t.Errorf("%s: titleDue = %v, want %v", c.name, got, c.due)
		}
	}
	for msg, want := range map[string]bool{
		"Hello": true, "hey there!": true, "Good morning team": true, "hi, how are you?": true, "yo": true,
		"Hello, can you make me a song about the dam?": false, "goodbye": false, "can you say hi": false,
		"good idea": false, "": false,
	} {
		if got := isBareGreeting(msg); got != want {
			t.Errorf("isBareGreeting(%q) = %v, want %v", msg, got, want)
		}
	}
}

func TestTheTitleReadsTheFirstThreeExchanges(t *testing.T) {
	s := asked("x", "x", "Hello", "make me a parody song about Mondays", "harder mix", "a fourth thing")
	// A struck reply is not what the conversation is about.
	s.Messages[3].Retracted = "wrong"
	s.Messages = append(s.Messages[:4], append([]ChatMessage{{Role: "assistant", Content: "the corrected reply"}}, s.Messages[4:]...)...)
	// Only the first reply to each message: a later bubble in the same turn is
	// more of the same answer.
	s.Messages = append(s.Messages[:7], append([]ChatMessage{{Role: "assistant", Content: "a second bubble"}}, s.Messages[7:]...)...)
	llm := &FakeLLM{Turns: []FakeTurn{{Content: "\"Monday Parody Song.\""}}}
	if got := generateSessionTitle(t.Context(), llm, s); got != "Monday Parody Song" {
		t.Errorf("title = %q", got)
	}
	p := llm.Prompt(0)
	for _, want := range []string{"User: Hello", "User: make me a parody song about Mondays", "User: harder mix",
		"Assistant: the corrected reply", "Assistant: reply to harder mix", "a greeting is not a topic"} {
		if !strings.Contains(p, want) {
			t.Errorf("the title prompt is missing %q:\n%s", want, p)
		}
	}
	for _, not := range []string{"a fourth thing", "reply to make me", "a second bubble"} {
		if strings.Contains(p, not) {
			t.Errorf("the title prompt should not carry %q:\n%s", not, p)
		}
	}
}

// The title call takes seconds. A turn that lands in them is kept, and a name
// the user gives in them wins.
func TestARetitleKeepsWhatLandedWhileItRan(t *testing.T) {
	for _, renamed := range []bool{false, true} {
		db := &DBase{Store: kvlite.MemStore()}
		s := asked("Hello", "Hello", "Hello", "make me a parody song about Mondays", "harder mix")
		if _, err := saveChatSession(db, s); err != nil {
			t.Fatal(err)
		}
		gate := make(chan struct{})
		llm := &FakeLLM{Turns: []FakeTurn{{Content: "Monday Parody Song", Wait: gate}}}
		app := &OrchestrateApp{}
		app.LLM = llm
		ct := &chatTurn{app: app, udb: db, agent: AgentRecord{ID: "a1"}, user: "u", session: &s}
		ct.retitleIfDue()
		waitFor(t, func() bool { return llm.Calls() == 1 })
		if err := appendToStoredSession(db, "a1", s.ID, s, ChatMessage{Role: "user", Content: "one more"}); err != nil {
			t.Fatal(err)
		}
		if renamed {
			renameChatSession(db, "a1", s.ID, "My Monday songs")
		}
		close(gate)
		if !renamed {
			waitFor(t, func() bool { got, _ := loadChatSession(db, "a1", s.ID); return got.Title == "Monday Parody Song" })
		} else {
			time.Sleep(300 * time.Millisecond)
		}
		got, _ := loadChatSession(db, "a1", s.ID)
		if n := len(got.Messages); n != 7 || got.Messages[n-1].Content != "one more" {
			t.Errorf("renamed=%v: the turn that landed during the title call was lost: %d messages", renamed, n)
		}
		switch {
		case renamed && got.Title != "My Monday songs":
			t.Errorf("the user's name was replaced with %q", got.Title)
		case !renamed && got.AutoTitle != "Monday Parody Song":
			t.Errorf("the automatic title was not recorded as automatic: %q", got.AutoTitle)
		}
	}
}

// A turn with no title due asks the model nothing: a long conversation is
// not renamed on every turn.
func TestATurnWithNoTitleDueAsksNothing(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	s := asked("x", "x", "Hello", "make me a parody song about Mondays", "harder mix", "thanks")
	if _, err := saveChatSession(db, s); err != nil {
		t.Fatal(err)
	}
	llm := &FakeLLM{Turns: []FakeTurn{{Content: "Another Title", Repeat: true}}}
	app := &OrchestrateApp{}
	app.LLM = llm
	(&chatTurn{app: app, udb: db, agent: AgentRecord{ID: "a1"}, user: "u", session: &s}).retitleIfDue()
	time.Sleep(200 * time.Millisecond)
	if n := llm.Calls(); n != 0 {
		t.Errorf("the model was asked for a title %d time(s) on a turn with none due", n)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
