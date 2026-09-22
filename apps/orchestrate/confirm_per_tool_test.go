package orchestrate

// A tool that asks before every call, on its own say-so.
//
// The escalation existed and was reachable one way only: a credential's
// "require confirm before each call" toggle, set per CREDENTIAL in the admin
// surface. So two tools on one key could not differ, a tool with no credential
// could not be confirmed at all, and the control lived nowhere near the agent
// whose calls it governed.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/tools/temptool"
	"github.com/cmcoffee/snugforge/kvlite"
)

func confirmTurn(t *testing.T, tools ...*TempTool) (*chatTurn, *ToolSession) {
	t.Helper()
	sess := &ToolSession{Username: "alice", TempTools: tools}
	// No sse: this turn has no interactive viewer, so an escalation denies.
	// That is what makes the DECISION visible in a unit test, since a card
	// nobody can answer is the same signal as a card that was never raised.
	return &chatTurn{user: "alice"}, sess
}

// The tool's own word arms the loop's check. Without this the loop never calls
// the hook at all and the flag is decoration.
func TestTheFlagArmsTheLoopsCheck(t *testing.T) {
	if !temptool.NeedsConfirm(&TempTool{Name: "post", CommandTemplate: "echo hi", ConfirmInChat: true}) {
		t.Error("a tool set to ask does not arm the loop's confirmation check")
	}
	// And a plain one still does not, so nothing that ran yesterday starts
	// asking today.
	if temptool.NeedsConfirm(&TempTool{Name: "read", CommandTemplate: "cat x"}) {
		t.Error("an ordinary shell tool started asking")
	}
}

// A tool with NO credential can now ask, which was impossible before: the
// credential toggle was the only route in.
func TestACredentiallessToolCanAsk(t *testing.T) {
	turn, sess := confirmTurn(t, &TempTool{Name: "wipe", CommandTemplate: "rm -rf x", ConfirmInChat: true})
	if turn.confirmFuncFor(sess)("wipe", `{"path":"x"}`) {
		t.Error("the call went through without asking anybody")
	}
	// Unflagged, same shape: unchanged, still allowed.
	turn2, sess2 := confirmTurn(t, &TempTool{Name: "read", CommandTemplate: "cat x"})
	if !turn2.confirmFuncFor(sess2)("read", "{}") {
		t.Error("an ordinary tool started being denied")
	}
}

// The tool's declaration beats what its credential happens to say. An owner
// who asked to be consulted about THIS tool is not overruled by a key that
// does not require it, and two tools on one key can now differ.
func TestTheToolsWordBeatsItsCredential(t *testing.T) {
	// For the secure store, which needs AuthDB wired.
	newTestOrchestrate(t)
	if err := Secure().Save(SecureCredential{Name: "confirm_per_tool_k", Type: SecureCredNone,
		Owner: "alice", AllowedURLPattern: "https://x.example/**"}, ""); err != nil {
		t.Skipf("no secure store here: %v", err)
	}
	// The credential does NOT require confirmation.
	quiet := &TempTool{Name: "quiet", CommandTemplate: "curl x", Credential: "confirm_per_tool_k"}
	loud := &TempTool{Name: "loud", CommandTemplate: "curl x", Credential: "confirm_per_tool_k", ConfirmInChat: true}

	turn, sess := confirmTurn(t, quiet, loud)
	fn := turn.confirmFuncFor(sess)
	if !fn("quiet", "{}") {
		t.Error("a tool on a quiet key started asking")
	}
	if fn("loud", "{}") {
		t.Error("the tool's own declaration was overruled by its credential")
	}
}

// Unattended, it is REFUSED rather than approved, and says so where a run's
// refusals are read. An approval nobody can give is not an approval.
func TestUnattendedItIsRefusedAndSaysSo(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	turn := logTurn(root, "alice", "alice", "a1", "s1")
	sess := &ToolSession{Username: "alice", TempTools: []*TempTool{
		{Name: "post", CommandTemplate: "curl x", ConfirmInChat: true},
	}}

	if turn.confirmFuncFor(sess)("post", "{}") {
		t.Fatal("a call nobody could approve was approved")
	}
	trail := decorateSessionDiags(parentTrailOf(UserDB(root, "alice"), "a1", "s1"))
	if len(trail) == 0 {
		t.Fatal("the refusal left no breadcrumb")
	}
	if !strings.Contains(trail[0].Detail, "no interactive viewer") {
		t.Errorf("the breadcrumb does not say why it was refused: %q", trail[0].Detail)
	}
}

// The decision is reviewable and revocable on the Permissions page, which is
// where every other standing decision lives.
func TestAnAskingToolShowsOnThePermissionsPage(t *testing.T) {
	app, udb, _ := newTestOrchestrate(t)
	pinRootDB(t)
	for _, tt := range []TempTool{
		{Name: "confirm_row_loud", CommandTemplate: "curl x", ConfirmInChat: true},
		{Name: "confirm_row_quiet", CommandTemplate: "echo hi"},
	} {
		if err := AdminPersistTempTool(udb, "alice", tt); err != nil {
			t.Fatal(err)
		}
	}
	rows := permRowsFor(t, app, "alice")
	row := rowByDetail(rows, "Asks before every call")
	if row == nil {
		t.Fatal("a tool set to ask is reviewable nowhere")
	}
	if row["Who"] != "confirm_row_loud" {
		t.Errorf("the row names the wrong subject: %+v", row)
	}
	if row["_policy"] != PolicyAsk {
		t.Errorf("the row does not read as asking: %+v", row)
	}
	// Blocked is the unattended mark and answers a different question, so it
	// is hidden rather than offered to mean something it does not.
	if row["_noblock"] != true {
		t.Errorf("the row offers a state that is not its question: %+v", row)
	}
	// The quiet tool DOES grow a row, reading "allow". It used to be hidden,
	// on the rule that a row appears only while its decision is load-bearing.
	// That rule made this control delete itself: the flag is a plain bool, so
	// "allow" and "never decided" are the same value, and clicking Always
	// allow removed the row you had just used. It reads as the click failing.
	//
	// It is also the wrong shape for a security page. "Supervised" means
	// nothing except next to the things that are not, and a page that lists
	// only the exceptions cannot answer what the exceptions are exceptions TO.
	quiet := rowByWho(rows, "confirm_row_quiet")
	if quiet == nil {
		t.Fatal("a tool with no supervision is missing, so the page cannot say what is unsupervised")
	}
	if quiet["_policy"] != PolicyAllow {
		t.Errorf("a tool that asks nothing should read as allow: %+v", quiet)
	}
}

// And it can be cleared from the same control that shows it.
func TestAnAskingToolCanBeQuietedFromThePage(t *testing.T) {
	app, udb, _ := newTestOrchestrate(t)
	pinRootDB(t)
	if err := AdminPersistTempTool(udb, "alice", TempTool{
		Name: "confirm_quiet_me", CommandTemplate: "curl x", ConfirmInChat: true}); err != nil {
		t.Fatal(err)
	}
	set := func(value string) {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, "/api/console/permissions/policy?id=confirmtool:confirm_quiet_me&value="+value, nil)
		w := httptest.NewRecorder()
		app.handleConsolePermissionPolicy(w, asUser(r, "alice"))
		if w.Code != http.StatusOK && w.Code != http.StatusNoContent {
			t.Fatalf("policy %s: %d %s", value, w.Code, w.Body.String())
		}
	}
	set(PolicyAllow)
	if toolAsks(t, udb, "confirm_quiet_me") {
		t.Error("allowing the tool left it asking")
	}
	set(PolicyAsk)
	if !toolAsks(t, udb, "confirm_quiet_me") {
		t.Error("setting it back to ask did not take")
	}
}

func toolAsks(t *testing.T, udb Database, name string) bool {
	t.Helper()
	for _, pt := range LoadPersistentTempTools(udb, "alice") {
		if pt.Tool.Name == name {
			return pt.Tool.ConfirmInChat
		}
	}
	t.Fatalf("no tool %q", name)
	return false
}

// A Builder edit must not silently clear it. The owner's decision about risk
// is not something a rewrite of the tool's body gets to undo.
func TestARewriteDoesNotQuietATool(t *testing.T) {
	_, udb, _ := newTestOrchestrate(t)
	if err := AdminPersistTempTool(udb, "alice", TempTool{
		Name: "confirm_survives", CommandTemplate: "curl x", ConfirmInChat: true}); err != nil {
		t.Fatal(err)
	}
	// The same tool re-persisted WITHOUT the flag, which is what an edit that
	// reconstructs the record looks like.
	if err := AdminPersistTempTool(udb, "alice", TempTool{
		Name: "confirm_survives", CommandTemplate: "curl y"}); err != nil {
		t.Fatal(err)
	}
	if !toolAsks(t, udb, "confirm_survives") {
		t.Error("a rewrite cleared the owner's decision")
	}
}

// --- The Tools modal's Ask control (api/tool-confirm) ---------------------

// The modal needs every tool the user owns a record for, not just the ones
// that ask: a name's ABSENCE is how a catalog row learns it is a framework
// tool with nothing to carry the flag.
func TestTheToolsModalCanSeeWhichToolsAsk(t *testing.T) {
	app, udb, _ := newTestOrchestrate(t)
	pinRootDB(t)
	for _, tt := range []TempTool{
		{Name: "modal_loud", CommandTemplate: "curl x", ConfirmInChat: true},
		{Name: "modal_quiet", CommandTemplate: "echo hi"},
	} {
		if err := AdminPersistTempTool(udb, "alice", tt); err != nil {
			t.Fatal(err)
		}
	}
	r := httptest.NewRequest(http.MethodGet, "/api/tool-confirm", nil)
	w := httptest.NewRecorder()
	app.handleToolConfirm(w, asUser(r, "alice"))
	if w.Code != http.StatusOK {
		t.Fatalf("GET: %d %s", w.Code, w.Body.String())
	}
	var got struct {
		Tools map[string]bool `json:"tools"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Tools["modal_loud"] {
		t.Error("a tool that asks does not read as asking")
	}
	asks, listed := got.Tools["modal_quiet"]
	if !listed {
		t.Error("a quiet tool of the user's is missing, so its row gets no control at all")
	}
	if asks {
		t.Error("a quiet tool reads as asking")
	}
	// Only the user's own records. A framework tool listed here would put a
	// switch on a row with nothing behind it to hold the setting.
	if len(got.Tools) != 2 {
		t.Errorf("the map carries more than the user's own tools: %v", got.Tools)
	}
}

// The control writes through the same setter the Permissions page uses, and
// both directions take.
func TestTheToolsModalCanSetAndClearAsk(t *testing.T) {
	app, udb, _ := newTestOrchestrate(t)
	pinRootDB(t)
	if err := AdminPersistTempTool(udb, "alice", TempTool{
		Name: "modal_toggle", CommandTemplate: "curl x"}); err != nil {
		t.Fatal(err)
	}
	set := func(on bool) {
		t.Helper()
		body := strings.NewReader(fmt.Sprintf(`{"name":"modal_toggle","on":%t}`, on))
		r := httptest.NewRequest(http.MethodPost, "/api/tool-confirm", body)
		w := httptest.NewRecorder()
		app.handleToolConfirm(w, asUser(r, "alice"))
		if w.Code != http.StatusNoContent {
			t.Fatalf("POST on=%t: %d %s", on, w.Code, w.Body.String())
		}
	}
	set(true)
	if !toolAsks(t, udb, "modal_toggle") {
		t.Error("turning the control on did not take")
	}
	set(false)
	if toolAsks(t, udb, "modal_toggle") {
		t.Error("turning the control off did not take")
	}
}

// A name with no record behind it is a miss, not a silent success: the modal
// repaints the button from the answer, so "no such tool" answered as 204 would
// paint a guard that is not there.
func TestAskingForAToolWithNoRecordIsAMiss(t *testing.T) {
	app, _, _ := newTestOrchestrate(t)
	pinRootDB(t)
	r := httptest.NewRequest(http.MethodPost, "/api/tool-confirm",
		strings.NewReader(`{"name":"no_such_tool_here","on":true}`))
	w := httptest.NewRecorder()
	app.handleToolConfirm(w, asUser(r, "alice"))
	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404 for a tool that does not exist, got %d %s", w.Code, w.Body.String())
	}
}

// Registered is not reachable. The handler is worth nothing if the modal never
// calls it, and the control is worth nothing on only one of the two lists a
// tool can appear in: the same tool must not answer "does this ask first"
// differently depending on where you found it.
func TestTheToolsModalReachesTheAskEndpointFromBothLists(t *testing.T) {
	routes, err := os.ReadFile("orchestrate.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(routes), `"/api/tool-confirm"`) {
		t.Error("the Ask endpoint is not registered, so nothing can reach it")
	}
	assets, err := os.ReadFile("assets/web_assets.html")
	if err != nil {
		t.Fatal(err)
	}
	src := string(assets)
	for _, want := range []string{
		"api/tool-confirm",          // the fetch exists at all
		"addAskControl(rowActions",  // scoped rows, beside Scope
		"addAskControl(poolActions", // the shared catalog rows
		"fetchToolConfirmMap()",     // state read once, with the agent
	} {
		if !strings.Contains(src, want) {
			t.Errorf("the Tools modal is missing %q, so the control is dead on that path", want)
		}
	}
}

// rowByWho finds a row by its subject.
func rowByWho(rows []map[string]any, who string) map[string]any {
	for _, r := range rows {
		if r["Who"] == who {
			return r
		}
	}
	return nil
}

// The framework's own tools can be marked. They carry no record, and the mark
// does not want one: it is keyed by NAME.
//
// This is the case that matters most and was impossible before. The flag lived
// on the tool record, so only a tool somebody had authored could hold it,
// which made web_search and browse_page - the searches, the browsing, the
// fetches - the only tools that could not be stopped on.
func TestAFrameworkToolCanBeMarkedToAsk(t *testing.T) {
	_, udb, _ := newTestOrchestrate(t)
	pinRootDB(t)
	if !SetUserToolAsksInChat(udb, "alice", "web_search", true) {
		t.Fatal("marking a tool with no record was refused")
	}
	if !UserToolAsksInChat(udb, "alice", "web_search") {
		t.Error("the mark did not stick")
	}
	// And it comes back off.
	SetUserToolAsksInChat(udb, "alice", "web_search", false)
	if UserToolAsksInChat(udb, "alice", "web_search") {
		t.Error("clearing the mark left it asking")
	}
}

// A mark made before the storage moved must not quietly stop working. The flag
// used to live on the tool record, and a tool marked then still has it there.
func TestAMarkOnAnOldToolRecordStillAsks(t *testing.T) {
	_, udb, _ := newTestOrchestrate(t)
	pinRootDB(t)
	if err := AdminPersistTempTool(udb, "alice", TempTool{
		Name: "legacy_marked", CommandTemplate: "curl x", ConfirmInChat: true}); err != nil {
		t.Fatal(err)
	}
	if !UserToolAsksInChat(udb, "alice", "legacy_marked") {
		t.Error("a tool marked under the old storage stopped asking when the storage moved")
	}
	// Listing finds it too, so a surface showing the marks shows that one.
	var found bool
	for _, n := range AskInChatTools(udb, "alice") {
		if n == "legacy_marked" {
			found = true
		}
	}
	if !found {
		t.Error("the listing misses a mark held on a tool record")
	}
	// Clearing it clears BOTH stores, or the reader resurrects what the owner
	// just removed.
	SetUserToolAsksInChat(udb, "alice", "legacy_marked", false)
	if UserToolAsksInChat(udb, "alice", "legacy_marked") {
		t.Error("clearing left the old flag set, so the tool goes on asking")
	}
}
