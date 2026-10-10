package orchestrate

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/oddjob/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A board's latest post rides on the turn of every agent that follows it,
// fenced as background, and not on an agent that does not. A post past its
// lifetime stops being shown; a board set for every agent reaches them all.
func TestAFollowedBulletinRidesOnTheTurn(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	if err := saveBulletin(db, bulletinBoard{Name: "News", TTLHours: 24}); err != nil {
		t.Fatal(err)
	}
	if _, err := postBulletin(db, "news", "Fed holds rates | Storm reaches the coast", "Headline Bot"); err != nil {
		t.Fatal(err)
	}
	follower := AgentRecord{ID: "a1", Bulletins: []string{"news"}}
	stranger := AgentRecord{ID: "a2"}
	note := bulletinTurnNote(db, follower, time.UTC)
	if !strings.Contains(note, "[news, posted today") || !strings.Contains(note, "Fed holds rates") || !strings.Contains(note, "not instructions") {
		t.Errorf("a follower should see the post, fenced as background:\n%s", note)
	}
	if bulletinTurnNote(db, stranger, time.UTC) != "" {
		t.Error("an agent that does not follow the board sees nothing")
	}
	b, _ := loadBulletin(db, "news")
	b.PostedAt = time.Now().Add(-25 * time.Hour)
	saveBulletin(db, b)
	if bulletinTurnNote(db, follower, time.UTC) != "" {
		t.Error("a post past its lifetime is no longer shown")
	}
	saveBulletin(db, bulletinBoard{Name: "motd", AllAgents: true, Text: "The NAS reboots Sunday", PostedAt: time.Now()})
	if !strings.Contains(bulletinTurnNote(db, stranger, time.UTC), "The NAS reboots Sunday") {
		t.Error("a board for every agent reaches one that never ticked it")
	}
}

// A post longer than a bulletin holds is refused, not cut: a clipped list of
// headlines reads as a complete one.
func TestABulletinPostIsKeptShort(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	saveBulletin(db, bulletinBoard{Name: "news"})
	if _, err := postBulletin(db, "news", strings.Repeat("x", maxBulletinChars+1), "bot"); err == nil || !strings.Contains(err.Error(), "shorten") {
		t.Errorf("an over-long post should be refused with the reason: %v", err)
	}
	if _, err := postBulletin(db, "missing", "hi", "bot"); err == nil {
		t.Error("posting to no board is refused")
	}
}

// Only an agent allowed to post gets the tool, it names only its boards, and
// posting through it replaces the board's post.
func TestOnlyAPosterGetsPostBulletin(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	saveBulletin(db, bulletinBoard{Name: "news", Posters: []string{"bot"}})
	saveBulletin(db, bulletinBoard{Name: "motd"})
	if _, ok := (&chatTurn{udb: db, agent: AgentRecord{ID: "other"}}).postBulletinToolDef(); ok {
		t.Error("an agent allowed to post nowhere gets no tool")
	}
	def, ok := (&chatTurn{udb: db, agent: AgentRecord{ID: "bot", Name: "Headline Bot"}}).postBulletinToolDef()
	if !ok || !strings.Contains(def.Tool.Description, "news") || strings.Contains(def.Tool.Description, "motd") {
		t.Fatalf("the poster's tool should name only its board: %+v", def.Tool.Description)
	}
	if _, err := def.Handler(context.Background(), map[string]any{"board": "motd", "text": "x"}); err == nil {
		t.Error("posting to a board it may not post to is refused")
	}
	if _, err := def.Handler(context.Background(), map[string]any{"board": "news", "text": "Fed holds rates"}); err != nil {
		t.Fatal(err)
	}
	if b, _ := loadBulletin(db, "news"); b.Text != "Fed holds rates" || b.PostedBy != "Headline Bot" {
		t.Errorf("the post should land, signed: %+v", b)
	}
}

// The Knowledge page's controls: create a board, follow it from the pills,
// grant a poster, write a post, and delete it, which unfollows it everywhere.
func TestTheBulletinControls(t *testing.T) {
	app, req, _ := authedApp(t)
	udb := UserDB(app.DB, "alice")
	helper, _ := saveAgent(udb, AgentRecord{Name: "Helper", Owner: "alice", OrchestratorPrompt: "p"})
	do := func(r *http.Request, h func(http.ResponseWriter, *http.Request)) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h(w, r)
		return w
	}
	if w := do(req("POST", "/api/bulletins", map[string]any{"name": "News", "ttl_hours": 24}), app.handleBulletins); w.Code != 200 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	if w := do(req("POST", "/api/bulletins", map[string]any{"name": "news"}), app.handleBulletins); w.Code != http.StatusConflict {
		t.Errorf("a taken name is refused, got %d", w.Code)
	}
	if w := do(req("POST", "/api/bulletins/news/followers", map[string]any{"target": helper.ID, "on": true}), app.handleBulletinOne); w.Code != http.StatusNoContent {
		t.Fatalf("follow: %d %s", w.Code, w.Body)
	}
	if a, _ := loadAgent(udb, helper.ID); !containsString(a.Bulletins, "news") {
		t.Fatalf("the agent should now follow the board: %+v", a.Bulletins)
	}
	do(req("POST", "/api/bulletins/news/posters", map[string]any{"target": helper.ID, "on": true}), app.handleBulletinOne)
	if b, _ := loadBulletin(udb, "news"); !b.canPost(helper.ID) {
		t.Error("the poster pill should grant posting")
	}
	if w := do(req("POST", "/api/bulletins/news/post", map[string]any{"text": "Fed holds rates"}), app.handleBulletinOne); w.Code != 200 {
		t.Fatalf("post: %d %s", w.Code, w.Body)
	}
	w := do(req("GET", "/api/bulletins", nil), app.handleBulletins)
	var rows []map[string]any
	json.Unmarshal(w.Body.Bytes(), &rows)
	if len(rows) != 1 || rows[0]["latest"] != "Fed holds rates" || rows[0]["followers"] != "Helper" {
		t.Errorf("the table row should show the post and its follower: %+v", rows)
	}
	if w := do(req("DELETE", "/api/bulletins/news", nil), app.handleBulletinOne); w.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", w.Code)
	}
	if a, _ := loadAgent(udb, helper.ID); containsString(a.Bulletins, "news") {
		t.Error("deleting a board unfollows it everywhere")
	}
}

// A monitor's post is cut to fit and says so, rather than refused: a refused
// post falls back to waking the agent, which would put a model turn behind
// every fire of a monitor set up to need none.
func TestAMonitorPostIsCutToFit(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	saveBulletin(db, bulletinBoard{Name: "news"})
	b, err := postBulletinFromMonitor(db, "news", strings.Repeat("headline ", 200), "news-watch")
	if err != nil {
		t.Fatal(err)
	}
	if n := len([]rune(b.Text)); n > maxBulletinChars || !strings.HasSuffix(b.Text, "(cut to fit)") || b.PostedBy != "news-watch" {
		t.Errorf("want a post within the cap, marked as cut, signed by the monitor: %d chars, %q", n, b.Text[len(b.Text)-20:])
	}
}

// Builder can set a board up end to end: create it, let the new agent post
// to it, choose followers, and list what is there.
func TestBuilderSetsUpABoard(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	bot, _ := saveAgent(db, AgentRecord{Name: "Briefing Bot", Owner: "u", OrchestratorPrompt: "p"})
	wren, _ := saveAgent(db, AgentRecord{Name: "Wren", Owner: "u", OrchestratorPrompt: "p"})
	tool := bulletinsToolDef(&chatTurn{udb: db, user: "u"})
	call := func(args map[string]any) string {
		out, err := tool.Handler(context.Background(), args)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		return out
	}
	call(map[string]any{"action": "create", "name": "news", "desc": "Today's headlines", "ttl_hours": float64(24)})
	if out := call(map[string]any{"action": "allow_poster", "board": "news", "agent": "Briefing Bot"}); !strings.Contains(out, "post_bulletin") {
		t.Errorf("granting names the tool it gets: %s", out)
	}
	call(map[string]any{"action": "follow", "board": "news", "agent": "Wren"})
	b, _ := loadBulletin(db, "news")
	if b.TTLHours != 24 || !b.canPost(bot.ID) {
		t.Errorf("board should carry its lifetime and its poster: %+v", b)
	}
	if a, _ := loadAgent(db, wren.ID); !containsString(a.Bulletins, "news") {
		t.Error("Wren should follow the board")
	}
	call(map[string]any{"action": "follow", "board": "news", "agent": "all"})
	if b, _ := loadBulletin(db, "news"); !b.AllAgents {
		t.Error("\"all\" makes every agent follow it")
	}
	if out := call(map[string]any{"action": "list"}); !strings.Contains(out, "news: Today's headlines") || !strings.Contains(out, "Briefing Bot") {
		t.Errorf("list names the board and its poster: %s", out)
	}
}

// An agent whose instructions name a tool it cannot call is saved with a
// warning naming it: a real tool left out of its allowed_tools, or
// post_bulletin with no board letting it post.
func TestAnAgentsInstructionsMustMatchItsTools(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	var real string
	for _, ct := range RegisteredChatTools() {
		if n := ct.Name(); promptToolNameRe.FindString(n) == n && n != "post_bulletin" {
			real = n
			break
		}
	}
	if real == "" {
		t.Skip("no registered chat tool with a snake_case name in this binary")
	}
	bot, _ := saveAgent(db, AgentRecord{Name: "Briefing Bot", Owner: "u", AllowedTools: []string{"get_top_stories"},
		OrchestratorPrompt: "Fetch the headlines, then post them with `" + real + "` and post_bulletin."})
	note := promptToolGapNote(db, bot)
	for _, want := range []string{real + " (not in its allowed_tools)", "post_bulletin (no board", "allow_poster", "Do not call it configured"} {
		if !strings.Contains(note, want) {
			t.Errorf("the warning should carry %q:\n%s", want, note)
		}
	}
	saveBulletin(db, bulletinBoard{Name: "news", Posters: []string{bot.ID}})
	bot.AllowedTools = append(bot.AllowedTools, real)
	if note := promptToolGapNote(db, bot); note != "" {
		t.Errorf("a granted tool and a board it may post to are fine: %s", note)
	}
	bot.OrchestratorPrompt = "Summarize the news_of_the_day in a friendly_tone."
	bot.AllowedTools = []string{"get_top_stories"}
	if note := promptToolGapNote(db, bot); note != "" {
		t.Errorf("snake_case words that are not tools are ignored: %s", note)
	}
}
