package orchestrate

import (
	"testing"
	"time"

	. "github.com/cmcoffee/oddjob/core"
	"github.com/cmcoffee/oddjob/core/buildledger"
	"github.com/cmcoffee/snugforge/kvlite"
)

// An app verify whose only failure was the page check not running found
// nothing wrong with the app: unproven, so an outage does not read as a
// build habit. With a real failure beside it, the run failed on that.
func TestAppVerifyOutageIsUnproven(t *testing.T) {
	buildledger.SetStore(&DBase{Store: kvlite.MemStore()})
	defer buildledger.SetStore(nil)
	turn := &chatTurn{user: "alice", agent: AgentRecord{ID: "builder"}, session: &ChatSession{ID: "s1"}}

	turn.recordAppVerify("dash", 1, nil, true)
	turn.recordAppVerify("dash", 2, []string{"script-fail"}, true)
	turn.recordAppVerify("dash", 0, nil, false)

	rep := buildledger.Read(time.Time{})
	if rep.Unproven != 1 || rep.Fails != 1 || rep.Passes != 1 {
		t.Fatalf("verdicts = %d unproven, %d failed, %d passed; want 1 each", rep.Unproven, rep.Fails, rep.Passes)
	}
	if rep.Episodes != 1 || !rep.Recent[0].Green || rep.Recent[0].Attempts != 3 || rep.Recent[0].Kind != buildledger.KindApp {
		t.Fatalf("episode = %+v", rep.Recent[0])
	}
}

// The stamper marks a round's verdicts with the model that served it, with the
// clauses from the turn's digest; a round with no tool calls ran no verify,
// and the loop's "unset" tier is not a tier.
func TestBuildStamperStampsToolRounds(t *testing.T) {
	buildledger.SetStore(&DBase{Store: kvlite.MemStore()})
	defer buildledger.SetStore(nil)
	turn := &chatTurn{user: "alice", agent: AgentRecord{ID: "builder"}, session: &ChatSession{ID: "s-stamp"}}
	bs := newBuildStamper(turn.chatSessionID())
	bs.digest(PromptDigest{ClauseKeys: []string{"verify_first"}})

	turn.recordAppVerify("dash", 1, []string{"render-blank"}, false)
	bs.step(StepInfo{Model: "qwen", Tier: "worker"}) // no tool calls: not this verify's round
	bs.step(StepInfo{Model: "qwen", Tier: "unset", ToolCalls: []ToolCall{{Name: "app_def"}}})

	rep := buildledger.Read(time.Time{})
	if len(rep.Tiers) != 1 || rep.Tiers[0].Tier != "unknown" || rep.Tiers[0].Model != "qwen" {
		t.Fatalf("tiers = %+v; want the model with no tier", rep.Tiers)
	}

	var nilStamper *buildStamper
	nilStamper.digest(PromptDigest{})
	nilStamper.step(StepInfo{Model: "x", ToolCalls: []ToolCall{{Name: "y"}}})
}
