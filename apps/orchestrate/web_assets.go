// App-specific UI bits loaded into the orchestrate chat page's <head>.
// Goals:
//
//  1. CSS + renderers for the in-chat plan, intent, and draft blocks —
//     visually 1:1 with servitor's equivalents (servitor_plan,
//     servitor_intent, servitor_draft). Both apps converge on the
//     same look so a user familiar with one immediately recognizes
//     the other.
//  2. CSS for interjection bubble affordances (edit/delete).
//  3. Client actions for the chat-page toolbar (edit/clone/etc).
//
// The markup itself lives in assets/web_assets.html (a <style> block followed
// by a <script> block) so it can be edited as real HTML/CSS/JS — highlighting,
// linting, formatting, and legal backticks/template literals — instead of a Go
// raw string. It is injected verbatim into the page <head> (see page_chat.go).

package orchestrate

import (
	_ "embed"
	"strings"
)

//go:embed assets/web_assets.html
var orchestrateWebAssets string

// ReportCardHead draws a message that arrived on its own (a scheduled report,
// a monitor wake, a channel message) as a titled card. Every page showing an
// agent's thread loads it: the console here, and the restricted chat on
// /agents/, which showed a wake's whole prompt as an ordinary bubble without
// it.
//
//go:embed assets/report_cards.html
var ReportCardHead string

// IntakeFormHead is the intake form an agent opens a new session with (its
// intake_form), for every page that shows an agent's chat: the console here
// and the restricted chat on /agents/, which had a copy of its own that never
// learned steps or live options, so Builder's form there could not be filled
// in. The page names the active agent with window.uiAgentIntakeSetAgent.
//
//go:embed assets/intake_form.html
var IntakeFormHead string

//go:embed assets/reply_flags.html
var replyFlagsHead string

// ReplyFlagsHead is the thumbs on an agent's replies (agent_feedback.go), for
// every page that shows an agent's chat: the console, and a shared agent's
// own page on /agents/, where the thumbs were missing. Both post to the one
// endpoint, here.
func ReplyFlagsHead() string {
	return strings.Replace(replyFlagsHead, "__REPLY_FLAG_URL__", orchPath("api/reply-flag"), 1)
}
