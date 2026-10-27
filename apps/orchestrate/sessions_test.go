package orchestrate

import (
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/changes"
	"github.com/cmcoffee/gohort/core/textutil"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A thread an external caller opens is named from what was said in it, at
// once and then by a written title; a run that does not ask keeps no name.
func TestAnAPIThreadIsNamedFromItsConversation(t *testing.T) {
	df := newDispatchFixture(t, FakeTurn{Content: "Order 42 ships Tuesday", Repeat: true})

	df.run(AgentSyncRun{SubSessionID: "ext:k:conv:1", Message: "hello", AutoTitle: true})
	if s := df.thread("ext:k:conv:1"); s.Title != "hello" || s.AutoTitle != "hello" {
		t.Errorf("a new thread should be named from its first message, got %q / %q", s.Title, s.AutoTitle)
	}

	df.run(AgentSyncRun{SubSessionID: "plain", Message: "hello"})
	if s := df.thread("plain"); s.Title != "" {
		t.Errorf("a run that does not ask for a name should leave none, got %q", s.Title)
	}

	const asked = "when does order 42 ship"
	df.run(AgentSyncRun{SubSessionID: "ext:k:conv:2", Message: asked, AutoTitle: true})
	deadline := time.Now().Add(3 * time.Second)
	for {
		s := df.thread("ext:k:conv:2")
		if s.Title != "" && s.Title != textutil.FirstMessageTitle(asked, sessionTitleChars) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the thread was never given a written title, still %q", s.Title)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A caller with no thread of its own continues the conversation it is in,
// and one that went quiet starts another.
func TestACallerContinuesItsRecentConversationOnly(t *testing.T) {
	df := newDispatchFixture(t)
	tbl := sessionTable(df.agent.ID)
	put := func(id string, ago time.Duration) {
		df.udb.Set(tbl, id, ChatSession{ID: id, AgentID: df.agent.ID, LastAt: time.Now().Add(-ago)})
	}
	const prefix = "ext:k:conv:"
	if got := df.app.RecentThread("u", df.agent.ID, prefix, 30*time.Minute); got != "" {
		t.Errorf("no conversation yet, got %q", got)
	}
	put(prefix+"a", 2*time.Minute)
	put(prefix+"b", 10*time.Minute)
	put("ext:j:conv:c", time.Second) // another key's, newer
	if got := df.app.RecentThread("u", df.agent.ID, prefix, 30*time.Minute); got != prefix+"a" {
		t.Errorf("the latest of this caller's conversations should continue, got %q", got)
	}
	put(prefix+"a", 2*time.Hour)
	put(prefix+"b", time.Hour)
	if got := df.app.RecentThread("u", df.agent.ID, prefix, 30*time.Minute); got != "" {
		t.Errorf("a conversation quiet past the pause should not continue, got %q", got)
	}
}

// A conversation that opened with "Hello" was named "Casual Greeting" from
// that greeting alone and kept it. A title now waits for an exchange with
// something in it, is written once more from the first three, and never
// replaces a name the user gave.

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

// A session saved, renamed, read or deleted wakes its owner's open pages to
// re-read their session lists (core/changes "sessions"), and nobody else's.

func TestSessionWritesWakeTheirOwnersPages(t *testing.T) {
	base := &DBase{Store: kvlite.MemStore()}
	udb := UserDB(base, "u-sess")
	mine, others := changes.ForUser("sessions", "u-sess"), changes.ForUser("sessions", "someone-else")
	moved := func(what string, do func()) {
		t.Helper()
		before, _ := changes.Version(mine)
		other, _ := changes.Version(others)
		do()
		if after, _ := changes.Version(mine); after == before {
			t.Errorf("%s did not wake the owner's pages", what)
		}
		if o, _ := changes.Version(others); o != other {
			t.Errorf("%s woke someone else's", what)
		}
	}
	var s ChatSession
	moved("a save", func() {
		var err error
		s, err = saveChatSession(udb, ChatSession{AgentID: "a1", Title: "t"})
		if err != nil {
			t.Fatal(err)
		}
	})
	moved("a rename", func() { renameChatSession(udb, "a1", s.ID, "renamed") })
	s.LastAt = time.Now().Add(time.Hour) // unread
	udb.Set(sessionTable("a1"), s.ID, s)
	moved("reading it", func() { markSessionSeen(udb, "a1", s.ID) })
	moved("a delete", func() { deleteChatSession(udb, "a1", s.ID) })
}
