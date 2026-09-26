// What an agent does about a broken tool: offer the user a Builder fix.
//
// A custom tool that failed used to leave the agent two moves, retrying (the
// give-up guard pushes that) or going around it, which is how an agent ended
// up calling a credential's API directly with guessed endpoints. Neither gets
// the tool fixed. So a failure that looks like the tool itself carries a note:
// stop working around it, tell the user what failed, and offer to have Builder
// fix it. Asked, not done: fixing a tool changes it for every agent that uses
// it, and it is the owner's tool, so the owner says yes. Work a contact
// started gets no offer (Builder answers to the owner only), and Builder,
// which fixes tools, needs none.

package orchestrate

import (
	"fmt"
	"sync"
)

// toolFailureNoteMarker follows the tool's name in the note, which is how a
// spilled result tells the note apart from the body it was appended to.
const toolFailureNoteMarker = " looks broken rather than your call"

// toolFailureAdvice is the ToolSession.ToolFailureAdvice for an agent run.
// interactive is whether the user can be asked on a card (ask_user); a
// channel, delegated or scheduled run says it in the reply instead.
// contactStarted is read at the moment of the failure, since the mark that
// says so can be set after the session is built. A failure with a why (a
// crash, a timeout, a server error, output cut short) is advised at once; one
// that could be a bad argument, on its second occurrence. Each tool is
// advised once per run.
func toolFailureAdvice(agent AgentRecord, interactive bool, contactStarted func() bool) func(string, string) string {
	if isBuilderAgent(agent.ID) {
		return nil
	}
	canBuilder := builderDispatchAllowed(agent)
	var mu sync.Mutex
	failures := map[string]int{}
	advised := map[string]bool{}
	return func(tool, why string) string {
		if contactStarted != nil && contactStarted() {
			return ""
		}
		mu.Lock()
		defer mu.Unlock()
		failures[tool]++
		if advised[tool] || (why == "" && failures[tool] < 2) {
			return ""
		}
		advised[tool] = true
		if why == "" {
			why = "it failed again"
		}
		head := fmt.Sprintf("Note: %s%s (%s). Do not keep retrying it, and do not work around it with other tools, scripts of your own or direct API calls. Do not present a partial result as the whole one.", tool, toolFailureNoteMarker, why)
		switch {
		case interactive && canBuilder:
			return head + fmt.Sprintf(" Tell the user plainly what failed and ask whether they want Builder to fix it (ask_user, Yes or No). If they say yes: agents(action=\"run\", agent=\"builder\", message=\"Fix the %s tool: <the arguments you called it with, this error, and what you were trying to do>\").", tool)
		case interactive:
			return head + fmt.Sprintf(" Tell the user plainly what failed, and that Builder can fix %s if they open it there.", tool)
		case canBuilder:
			return head + fmt.Sprintf(" Say plainly in your reply that %s is failing and offer to have Builder fix it.", tool)
		}
		return head + fmt.Sprintf(" Say plainly in your reply that %s is failing, and that Builder can fix it.", tool)
	}
}
