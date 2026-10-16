package orchestrate

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func sessionMarkerStore(t *testing.T) Database {
	t.Helper()
	InvalidateChunkCache()
	t.Cleanup(InvalidateChunkCache)
	return &DBase{Store: kvlite.MemStore()}
}

// saveTestSession stores a session with these user/assistant exchanges.
func saveTestSession(t *testing.T, db Database, agentID, id, title string, exchanges ...string) ChatSession {
	t.Helper()
	s := ChatSession{ID: id, AgentID: agentID, Title: title, Created: time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)}
	for i, text := range exchanges {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		s.Messages = append(s.Messages, ChatMessage{Role: role, Content: text})
	}
	saved, err := saveChatSession(db, s)
	if err != nil {
		t.Fatal(err)
	}
	saved.Created = s.Created
	return saved
}

// On in chatbot mode, off in agent mode, and the agent's own setting wins.
func TestSessionLinksFollowTheMemoryMode(t *testing.T) {
	cases := []struct {
		rec  AgentRecord
		want bool
	}{
		{AgentRecord{MemoryMode: "chatbot"}, true},
		{AgentRecord{MemoryMode: "agent"}, false},
		{AgentRecord{}, false},
		{AgentRecord{MemoryMode: "agent", SessionLinks: "on"}, true},
		{AgentRecord{MemoryMode: "chatbot", SessionLinks: "off"}, false},
	}
	for _, c := range cases {
		if got := sessionLinksOn(c.rec); got != c.want {
			t.Errorf("%+v: got %v, want %v", c.rec, got, c.want)
		}
	}
	if normalizeSessionLinks(" On ") != "on" || normalizeSessionLinks("maybe") != "" || normalizeSessionLinks("default") != "" {
		t.Error("session_links is not normalized")
	}
}

// A marker holds the person's own words (the opening and the latest asks,
// trimmed), what the session made or changed across turns, and what it
// delivered. Nothing in it is written by a model.
func TestASessionMarkerIsBuiltFromTheRecord(t *testing.T) {
	s := ChatSession{ID: "s1", AgentID: "ag", Created: time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC),
		LastAt: time.Date(2026, 10, 9, 9, 0, 0, 0, time.UTC)}
	for i := 1; i <= 14; i++ {
		s.Messages = append(s.Messages, ChatMessage{Role: "user", Content: fmt.Sprintf("ask number %d", i)})
	}
	s.Messages = append(s.Messages,
		ChatMessage{Role: "user", Content: "a hidden one", Hidden: true},
		ChatMessage{Role: "user", Content: strings.Repeat("long ", 100)},
		ChatMessage{Role: "assistant", Content: "here it is", Files: []deliveredFile{{ID: "f1", Name: "song.mp3", Kind: "audio"}}})

	m := updatedSessionMarker(sessionMarker{Touched: []string{"tool:wiki_read"}}, s, []string{"app:swing", "tool:wiki_read"})
	if m.Title != "ask number 1" {
		t.Errorf("an untitled session's marker is titled %q, want its first ask", m.Title)
	}
	if strings.Join(m.Touched, ",") != "app:swing,tool:wiki_read" {
		t.Errorf("touched = %v, want the union of the turns", m.Touched)
	}
	text := sessionMarkerText(m, s)
	for _, want := range []string{"2026-10-03 to 2026-10-09", "ask number 1\n", "ask number 3\n", "ask number 14\n",
		"Made or changed: app swing, tool wiki_read", "Files delivered: song.mp3"} {
		if !strings.Contains(text, want) {
			t.Errorf("marker text lacks %q:\n%s", want, text)
		}
	}
	for _, not := range []string{"ask number 4\n", "a hidden one", strings.Repeat("long ", 60)} {
		if strings.Contains(text, not) {
			t.Errorf("marker text carries %q:\n%s", not, text)
		}
	}
}

// recall finds an earlier session by what was said in it, hands back an id
// that reads the conversation itself with a link to it, and never offers the
// session it is asked from.
func TestRecallFindsAnEarlierSession(t *testing.T) {
	db := sessionMarkerStore(t)
	agent := AgentRecord{ID: "ag", MemoryMode: "chatbot"}
	old := saveTestSession(t, db, "ag", "s-old", "Screener fix",
		"the swing screener drops tickers with a dot in them", "Fixed: the symbol parser now keeps BRK.B.")
	writeSessionMarker(context.Background(), db, old, []string{"app:swing"})
	cur := saveTestSession(t, db, "ag", "s-now", "Today", "what did we do about the screener tickers?")
	writeSessionMarker(context.Background(), db, cur, nil)

	turn := &chatTurn{user: "u", udb: db, agent: agent, session: &cur}
	out, err := turn.recallSearch("screener tickers", map[string]any{"layer": "session"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "[session] \"Screener fix\"") || !strings.Contains(out, "id: sess:s-old") ||
		!strings.Contains(out, "link: /orchestrate/?agent=ag&session=s-old") {
		t.Fatalf("recall did not return the earlier session:\n%s", out)
	}
	if strings.Contains(out, "sess:s-now") {
		t.Errorf("recall offered the session it was asked from:\n%s", out)
	}
	turn.noteIssuedRecallIDs(out)
	read, err := turn.recallFetch("sess:s-old")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read, "Them: the swing screener drops tickers") || !strings.Contains(read, "You: Fixed: the symbol parser") ||
		!strings.Contains(read, "/orchestrate/?agent=ag&session=s-old") || !strings.Contains(read, "the newer one holds") {
		t.Errorf("reading the hit did not return the conversation:\n%s", read)
	}
}

// With session links off, in an incognito session, or for an agent that
// answers from its corpus alone, recall does not search sessions, and no
// marker is written.
func TestSessionLinksStayOutWhenOff(t *testing.T) {
	db := sessionMarkerStore(t)
	cases := map[string]*chatTurn{
		"agent mode":  {udb: db, agent: AgentRecord{ID: "ag", MemoryMode: "agent"}, session: &ChatSession{ID: "s1"}},
		"turned off":  {udb: db, agent: AgentRecord{ID: "ag", MemoryMode: "chatbot", SessionLinks: "off"}, session: &ChatSession{ID: "s1"}},
		"incognito":   {udb: db, agent: AgentRecord{ID: "ag", MemoryMode: "chatbot"}, session: &ChatSession{ID: "s1", Incognito: true}},
		"corpus only": {udb: db, agent: AgentRecord{ID: "ag", MemoryMode: "chatbot", DisableExplicit: true, DisableInferred: true}, session: &ChatSession{ID: "s1"}},
	}
	for name, turn := range cases {
		if turn.recallLayerSet(nil)["session"] {
			t.Errorf("%s: recall searches sessions", name)
		}
		if strings.Contains(turn.recallToolDef().Tool.Description, "[session]") {
			t.Errorf("%s: recall describes [session]", name)
		}
		if out, _ := turn.recallFetch("sess:s1"); strings.Contains(out, "earlier session") {
			t.Errorf("%s: a session was read: %s", name, out)
		}
	}
	on := &chatTurn{udb: db, agent: AgentRecord{ID: "ag", MemoryMode: "chatbot"}, session: &ChatSession{ID: "s1"}}
	if !on.recallLayerSet(nil)["session"] || !strings.Contains(on.recallToolDef().Tool.Description, "[session]") {
		t.Error("a chatbot agent does not search sessions")
	}
	if strings.Contains(on.recallToolDef().Tool.Description, "—") {
		t.Error("the recall description carries an em dash")
	}
	// A Clean turn leaves no marker, and neither does the cortex thread.
	clean := &chatTurn{user: "u", udb: db, agent: AgentRecord{ID: "ag", MemoryMode: "chatbot"}, session: &ChatSession{ID: "s1"}, inferredDisabled: true}
	clean.noteSessionMarker()
	cortex := &chatTurn{user: "u", udb: db, agent: AgentRecord{ID: "ag", MemoryMode: "chatbot"}, session: &ChatSession{ID: cortexSessionID("ag")}}
	cortex.noteSessionMarker() // both refuse before starting the write
	if keys := db.Keys(sessionMarkerTable("ag")); len(keys) != 0 {
		t.Errorf("markers written: %v", keys)
	}
}

// Deleting a session deletes its marker; deleting the agent deletes all of
// them and leaves another agent's alone, including one whose id starts the
// same way.
func TestAMarkerGoesWithItsSession(t *testing.T) {
	db := sessionMarkerStore(t)
	for _, a := range []string{"a1", "a10"} {
		for _, id := range []string{"s1", "s2"} {
			s := saveTestSession(t, db, a, id, "t", "something about gardening")
			writeSessionMarker(context.Background(), db, s, nil)
		}
	}
	count := func(agentID string) (rows, chunks int) {
		rows = len(db.Keys(sessionMarkerTable(agentID)))
		for _, k := range db.Keys(EmbeddedChunks) {
			var c EmbeddedChunk
			if db.Get(EmbeddedChunks, k, &c) && c.Source == sessionMarkerSource(agentID) {
				chunks++
			}
		}
		return
	}
	if r, c := count("a1"); r != 2 || c < 2 {
		t.Fatalf("seeded a1 with %d markers and %d chunks", r, c)
	}
	deleteChatSession(db, "a1", "s1")
	if r, c := count("a1"); r != 1 || c < 1 {
		t.Errorf("after deleting one session: %d markers, %d chunks", r, c)
	}
	dropChatSessionBucket(db, "a1")
	if r, c := count("a1"); r != 0 || c != 0 {
		t.Errorf("after deleting the agent: %d markers, %d chunks", r, c)
	}
	if r, c := count("a10"); r != 2 || c < 2 {
		t.Errorf("another agent lost markers: %d markers, %d chunks", r, c)
	}
	// A marker whose session went some other way is dropped when read.
	turn := &chatTurn{user: "u", udb: db, agent: AgentRecord{ID: "a10", MemoryMode: "chatbot"}, session: &ChatSession{ID: "s-now"}}
	db.Unset(sessionTable("a10"), "s2")
	if out := turn.readSessionForRecall("s2"); !strings.Contains(out, "no longer exists") {
		t.Errorf("a deleted session read as %q", out)
	}
	if r, _ := count("a10"); r != 1 {
		t.Errorf("the dead marker was kept: %d markers", r)
	}
}

// A long session reads as its opening and as much of its end as fits, saying
// what was left out between them.
func TestALongSessionReadsHeadAndTail(t *testing.T) {
	var lines []string
	for i := 0; i < 50; i++ {
		lines = append(lines, fmt.Sprintf("line %02d %s", i, strings.Repeat("x", 90)))
	}
	out := fitSessionLines(lines, 2000)
	if len(out) > 2100 {
		t.Errorf("read is %d chars, budget 2000", len(out))
	}
	for _, want := range []string{"line 00", "line 01", "line 49", "messages left out"} {
		if !strings.Contains(out, want) {
			t.Errorf("read lacks %q", want)
		}
	}
	if strings.Contains(out, "line 10 ") {
		t.Error("the middle was kept")
	}
}
