package orchestrate

// A tool that asks before every call, on its own say-so.
//
// The escalation existed and was reachable one way only: a credential's
// "require confirm before each call" toggle, set per CREDENTIAL in the admin
// surface. So two tools on one key could not differ, a tool with no credential
// could not be confirmed at all, and the control lived nowhere near the agent
// whose calls it governed.

import (
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
