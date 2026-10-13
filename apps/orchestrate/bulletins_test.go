package orchestrate

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
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
	if _, ok := (&chatTurn{udb: db, agent: AgentRecord{ID: "other"}}).bulletinBoardToolDef(); ok {
		t.Error("an agent allowed to post nowhere gets no tool")
	}
	def, ok := (&chatTurn{udb: db, agent: AgentRecord{ID: "bot", Name: "Headline Bot"}}).bulletinBoardToolDef()
	if !ok || !strings.Contains(def.Tool.Description, "news") || strings.Contains(def.Tool.Description, "motd") {
		t.Fatalf("the poster's tool should name only its board: %+v", def.Tool.Description)
	}
	if _, err := def.Handler(context.Background(), map[string]any{"board": "motd", "action": "post", "text": "x"}); err == nil {
		t.Error("posting to a board it may not post to is refused")
	}
	if _, err := def.Handler(context.Background(), map[string]any{"board": "news", "action": "post", "text": "Fed holds rates"}); err != nil {
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
	if out := call(map[string]any{"action": "allow_poster", "board": "news", "agent": "Briefing Bot"}); !strings.Contains(out, "bulletin_board") {
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
// bulletin_board (or its old name, post_bulletin) with no board letting it
// post.
func TestAnAgentsInstructionsMustMatchItsTools(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	var real string
	for _, ct := range RegisteredChatTools() {
		if n := ct.Name(); promptToolNameRe.FindString(n) == n && n != bulletinToolName && n != legacyBulletinToolName {
			real = n
			break
		}
	}
	if real == "" {
		t.Skip("no registered chat tool with a snake_case name in this binary")
	}
	bot, _ := saveAgent(db, AgentRecord{Name: "Briefing Bot", Owner: "u", AllowedTools: []string{"get_top_stories"},
		OrchestratorPrompt: "Fetch the headlines, then post them with `" + real + "` and bulletin_board."})
	note := promptToolGapNote(db, bot)
	for _, want := range []string{real + " (not in its allowed_tools)", "bulletin_board (no board", "allow_poster", "Do not call it configured"} {
		if !strings.Contains(note, want) {
			t.Errorf("the warning should carry %q:\n%s", want, note)
		}
	}
	// Instructions written before the rename name the old tool, and are
	// read as naming this one.
	old := bot
	old.OrchestratorPrompt = "Post the headlines with post_bulletin."
	if note := promptToolGapNote(db, old); !strings.Contains(note, "post_bulletin (no board") {
		t.Errorf("the old name should be checked like the new one:\n%s", note)
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

// A sub-agent is listed nowhere a board is set up, so posting rights live on
// its parent and cover the agents it owns: the sub-agent gets bulletin_board
// under the parent's right, a grant aimed at the sub-agent lands on the
// parent, and the post names the parent with the agent that wrote it.
func TestASubAgentPostsUnderItsParentsRight(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	wren, _ := saveAgent(db, AgentRecord{Name: "Wren", Owner: "u", OrchestratorPrompt: "p"})
	radar, _ := saveAgent(db, AgentRecord{Name: "Local Radar", Owner: "u", OrchestratorPrompt: "p", OwnedBy: wren.ID, Hidden: true})
	tool := bulletinsToolDef(&chatTurn{udb: db, user: "u"})
	if _, err := tool.Handler(context.Background(), map[string]any{"action": "create", "name": "local-radar"}); err != nil {
		t.Fatal(err)
	}
	out, err := tool.Handler(context.Background(), map[string]any{"action": "allow_poster", "board": "local-radar", "agent": "Local Radar"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Wren may now post") || !strings.Contains(out, "sub-agent of Wren") {
		t.Fatalf("the grant should land on the parent and say so, got %q", out)
	}
	b, _ := loadBulletin(db, "local-radar")
	if !b.canPost(wren.ID) || b.canPost(radar.ID) {
		t.Fatalf("the board should name the parent, not the sub-agent: %v", b.Posters)
	}
	if !b.allowsAgent(radar) || !b.allowsAgent(wren) {
		t.Fatal("both the parent and its sub-agent may post")
	}
	if b.allowsAgent(AgentRecord{ID: "stranger", OwnedBy: "someone-else"}) {
		t.Fatal("an agent owned by a non-poster may not post")
	}

	post, ok := (&chatTurn{udb: db, user: "u", agent: radar}).bulletinBoardToolDef()
	if !ok {
		t.Fatal("the sub-agent should have bulletin_board under its parent's right")
	}
	if _, err := post.Handler(context.Background(), map[string]any{"board": "local-radar", "action": "post", "text": "Fog out by 11"}); err != nil {
		t.Fatal(err)
	}
	if b, _ := loadBulletin(db, "local-radar"); b.PostedBy != "Wren (Local Radar)" {
		t.Fatalf("the post should name the parent with the writer, got %q", b.PostedBy)
	}
}

// A board can be emptied: by a poster through bulletin_board(action=clear), and
// by the owner from the Knowledge page. Followers then see nothing from it,
// the row says who cleared it and when, and the next post fills it again. A
// post with no text points at clear rather than leaving the agent to look
// for another way.
func TestABulletinCanBeCleared(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	saveBulletin(db, bulletinBoard{Name: "radar", Posters: []string{"bot"}})
	follower := AgentRecord{ID: "a1", Bulletins: []string{"radar"}}
	def, ok := (&chatTurn{udb: db, agent: AgentRecord{ID: "bot", Name: "Radar Bot"}}).bulletinBoardToolDef()
	if !ok || def.Tool.Name != "bulletin_board" || !strings.HasPrefix(def.Tool.Description, "Post to or clear a bulletin board") {
		t.Fatalf("the tool should say up front that it posts and clears: %s %s", def.Tool.Name, def.Tool.Description)
	}
	if strings.Join(def.Tool.Required, ",") != "board,action" || strings.Join(def.Tool.Parameters["action"].Enum, ",") != "post,clear" {
		t.Errorf("board and action are required, action is post or clear: %v %v", def.Tool.Required, def.Tool.Parameters["action"].Enum)
	}
	if _, err := def.Handler(context.Background(), map[string]any{"board": "radar", "action": "post", "text": "Fog until noon"}); err != nil {
		t.Fatal(err)
	}
	if _, err := def.Handler(context.Background(), map[string]any{"board": "radar", "action": "post"}); err == nil || !strings.Contains(err.Error(), "action=\"clear\"") {
		t.Errorf("a post with no text should point at clear: %v", err)
	}
	if _, err := def.Handler(context.Background(), map[string]any{"board": "radar", "action": "wipe"}); err == nil {
		t.Error("an unknown action is refused")
	}
	if out, err := def.Handler(context.Background(), map[string]any{"board": "radar", "action": "clear"}); err != nil || !strings.Contains(out, "Cleared") {
		t.Fatalf("clear: %q %v", out, err)
	}
	b, _ := loadBulletin(db, "radar")
	if b.Text != "" || !b.Cleared || b.PostedBy != "Radar Bot" {
		t.Errorf("the board should be empty and say who cleared it: %+v", b)
	}
	if bulletinTurnNote(db, follower, time.UTC) != "" {
		t.Error("a cleared board still rides on its followers' turns")
	}
	row := bulletinRow(b, nil, time.UTC)
	if !strings.HasPrefix(row["posted"].(string), "Cleared") || row["_has_post"] != false {
		t.Errorf("the row should say it was cleared and offer no Clear: %+v", row)
	}
	if _, err := postBulletin(db, "radar", "Sun by two", "Radar Bot"); err != nil {
		t.Fatal(err)
	}
	if b, _ := loadBulletin(db, "radar"); b.Cleared || b.Text != "Sun by two" {
		t.Errorf("the next post fills the board again: %+v", b)
	}
	if _, err := def.Handler(context.Background(), map[string]any{"board": "other", "action": "clear"}); err == nil {
		t.Error("clearing a board it may not post to is refused")
	}
}

func TestTheOwnerClearsABoard(t *testing.T) {
	app, req, _ := authedApp(t)
	udb := UserDB(app.DB, "alice")
	saveBulletin(udb, bulletinBoard{Name: "radar"})
	postBulletin(udb, "radar", "Fog until noon", "you")
	w := httptest.NewRecorder()
	app.handleBulletinOne(w, req("POST", "/api/bulletins/radar/clear", nil))
	if w.Code != 200 {
		t.Fatalf("clear: %d %s", w.Code, w.Body)
	}
	if b, _ := loadBulletin(udb, "radar"); b.Text != "" || !b.Cleared {
		t.Errorf("the owner's Clear should empty the board: %+v", b)
	}
	w = httptest.NewRecorder()
	app.handleBulletinOne(w, req("GET", "/api/bulletins/radar/clear", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("clear is a POST, got %d", w.Code)
	}
}

// A follower that cannot post is told who can, on the board's line and when
// it searches for a way to post or clear; a poster's search is pointed at the
// tool it already has, which no index holds. Observed: a follower asked to
// clear a board searched five times and then went looking for its file.
func TestAFollowerLearnsWhoPostsToABoard(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	radar, _ := saveAgent(db, AgentRecord{Name: "Local Radar", Owner: "u", OrchestratorPrompt: "p"})
	wren, _ := saveAgent(db, AgentRecord{Name: "Wren", Owner: "u", OrchestratorPrompt: "p", Bulletins: []string{"local-radar", "motd"}})
	saveBulletin(db, bulletinBoard{Name: "local-radar", Posters: []string{radar.ID}})
	saveBulletin(db, bulletinBoard{Name: "motd"})
	postBulletin(db, "local-radar", "Fog until noon", "Local Radar")
	postBulletin(db, "motd", "The NAS reboots Sunday", "you")

	lines := strings.Join(bulletinLines(db, wren, time.UTC), "\n")
	if !strings.Contains(lines, "posted and cleared by Local Radar] Fog until noon") || !strings.Contains(lines, "posted and cleared by only the owner] The NAS") {
		t.Errorf("a follower's lines should say who posts:\n%s", lines)
	}
	radar.Bulletins = []string{"local-radar"}
	if strings.Contains(strings.Join(bulletinLines(db, radar, time.UTC), ""), "posted and cleared by") {
		t.Error("a poster's own line needs no note: the board is in its tool")
	}

	follower := &chatTurn{udb: db, user: "u", agent: wren}
	note := follower.bulletinFindNote("clear bulletin board")
	for _, want := range []string{"bulletin_board tool", "you are on none", "local-radar: Local Radar", "Knowledge > Bulletins", "not a file"} {
		if !strings.Contains(note, want) {
			t.Errorf("a follower's search should carry %q:\n%s", want, note)
		}
	}
	if note := (&chatTurn{udb: db, user: "u", agent: radar}).bulletinFindNote("post bulletin"); !strings.Contains(note, "already in your tools") || !strings.Contains(note, "local-radar") {
		t.Errorf("a poster's search should point at its tool: %s", note)
	}
	if follower.bulletinFindNote("resize an image") != "" {
		t.Error("a search about something else gets no note")
	}
}
