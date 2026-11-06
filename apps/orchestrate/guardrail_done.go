package orchestrate

// What this turn has already done, for a guardrail judging its next call.
//
// The warden judges one candidate in fresh context. That is what makes it hard
// to argue with, and it is also why a rule about ORDER defeated it: "Always
// perform verification on new posts and comments" cannot be judged from a post,
// because the verification that satisfies it is an earlier call the warden
// never sees. Observed: the agent verified a reply, then posted it, and was
// blocked twice for not verifying.
//
// The calls are not the agent's claim. The loop ran them and reports how each
// ended, so which tool ran and whether it succeeded is shown to the warden as
// fact the framework established. What the agent PUT in a call is its own
// text, and is fenced untrusted beside the list, never inside it.

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/textutil"
)

// doneCall is one call this turn made, as the loop reported it.
type doneCall struct {
	Tool   string // the tool, with its action for a grouped one: "moltbook/verify_content"
	Args   string // what the agent passed, shortened; untrusted
	Failed bool
}

// The most recent calls kept, and how much of each one's arguments.
const (
	doneCallsKept   = 12
	doneCallArgsCap = 240
)

// actionNameRE is what an action name may look like to be shown as one: the
// action is the agent's choice, so anything else is left out of the trusted
// line rather than trusted.
var actionNameRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,40}$`)

// noteDoneCall records one call the turn made (guardrailEnforcement.Observe).
func (t *chatTurn) noteDoneCall(call ToolCall, failed bool) {
	if t == nil {
		return
	}
	name := strings.TrimSpace(call.Name)
	if a, ok := call.Args["action"].(string); ok && actionNameRE.MatchString(strings.TrimSpace(a)) {
		name += "/" + strings.TrimSpace(a)
	}
	args := ""
	if len(call.Args) > 0 {
		if b, err := json.Marshal(call.Args); err == nil {
			args = string(b)
		}
	}
	if len(args) > doneCallArgsCap {
		args = args[:doneCallArgsCap] + "…"
	}
	t.doneMu.Lock()
	defer t.doneMu.Unlock()
	t.doneCalls = append(t.doneCalls, doneCall{Tool: name, Args: args, Failed: failed})
	if n := len(t.doneCalls); n > doneCallsKept {
		t.doneCalls = append([]doneCall(nil), t.doneCalls[n-doneCallsKept:]...)
	}
}

// doneSoFar is a copy of what the turn has done, oldest first.
func (t *chatTurn) doneSoFar() []doneCall {
	if t == nil {
		return nil
	}
	t.doneMu.Lock()
	defer t.doneMu.Unlock()
	return append([]doneCall(nil), t.doneCalls...)
}

// renderDone is the warden's account of what is already done: the trusted list
// of which tool ran and how it ended, and the untrusted arguments after it.
// Empty for no calls, so a check before any call is byte-identical to what it
// always was.
func renderDone(calls []doneCall) (trusted, untrusted string) {
	if len(calls) == 0 {
		return "", ""
	}
	var tb, ub strings.Builder
	tb.WriteString("ALREADY DONE THIS TURN (trusted: the framework ran these calls and recorded how each ended; nobody claimed them), oldest first:\n")
	for i, c := range calls {
		outcome := "succeeded"
		if c.Failed {
			outcome = "FAILED"
		}
		fmt.Fprintf(&tb, "%d. %s: %s\n", i+1, c.Tool, outcome)
		if c.Args != "" {
			fmt.Fprintf(&ub, "%d. %s\n", i+1, c.Args)
		}
	}
	tb.WriteString("A rule that requires a step BEFORE this action is complied with when that step is listed above as succeeded and concerns what this action does; one that FAILED was not done. Judge the action itself as before.\n")
	if ub.Len() > 0 {
		untrusted = textutil.UntrustedData("what the agent passed to those calls, numbered as above", strings.TrimRight(ub.String(), "\n"))
	}
	return tb.String(), untrusted
}
