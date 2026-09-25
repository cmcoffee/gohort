package orchestrate

// A delegated Builder can ask.
//
// ask_user is a control tool of the web runner (planRun): it records the
// question and ends the turn, and the page draws a card the user answers.
// None of that exists when another agent sends Builder a brief, so the dispatch
// catalog never had the tool, and the brief was wrapped in "no back-and-forth,
// make reasonable defaults". Builder guessed the account, the design and the
// schedule, and what it guessed went live as the answer.
//
// The relay this enables: Builder asks, the run ends, the question goes back
// to the agent that sent the brief as a question for the user, that agent puts
// it to the user, and their answer returns to Builder as the next message in
// the same thread (dispatch:<session>:<builder>), which is why Builder picks up
// where it stopped rather than starting over.
//
// Only where the answer can come back: an agents(run) dispatch, inline or
// handed off, keeps that thread. The approval-queued paths (delegate,
// request_build) run in a sub-session that is torn down on return, so there
// an answer would land in a fresh Builder with no memory of asking, and those
// keep the headless wrapper.

import (
	"context"
	"strings"
	"sync"

	. "github.com/cmcoffee/gohort/core"
)

// delegatedQuestion holds what a delegated Builder asked, for the caller to
// turn into the run's result.
type delegatedQuestion struct {
	mu       sync.Mutex
	question string
	options  []string
}

func (q *delegatedQuestion) asked() bool {
	if q == nil {
		return false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.question != ""
}

// optionsLine is the choices, on one line, or "" when there are none.
func (q *delegatedQuestion) optionsLine() string {
	if len(q.options) == 0 {
		return ""
	}
	return "\nOptions: " + strings.Join(q.options, " | ")
}

// forThread is the question as Builder's own turn in its thread, so the answer
// that arrives next reads as a reply to it.
func (q *delegatedQuestion) forThread() string {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.question + q.optionsLine()
}

// delegatedRelay is the question as the run's result: what to ask, and how to
// send the answer back so it reaches this thread. question is the forThread
// text after whatever the caller's output guard made of it.
func delegatedRelay(builderName, question string) string {
	return builderName + " needs an answer from the user before it can go on:\n\n" + question +
		"\n\nPut this to the user as it stands, without answering it yourself (ask_user with these options where you have it). " +
		"When they reply, send their answer to " + builderName + " with agents(action=\"run\") from this conversation: it picks up where it stopped."
}

// delegatedAskUserTool is ask_user for a Builder run another agent started.
// Listed in the run's RoundAbortTools, so a call ends the run.
func delegatedAskUserTool(q *delegatedQuestion) AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name: "ask_user",
			Description: "Ask the user something only they can settle, when you cannot go on without it: which account or instance, or a choice between meaningfully different designs. " +
				"This run ends when you call it. The question goes to the agent that sent the brief, it asks the user, and their answer comes back as the next message in this thread. " +
				"Ask once, with everything you need in one question, and never for what you can look up, probe or reasonably decide.",
			Parameters: map[string]ToolParam{
				"question": {Type: "string", Description: "The question, in plain text, as the user should read it."},
				"options": {
					Type:        "array",
					Description: "The answers to choose between, when the choice is bounded: one short label each.",
					Items:       &ToolParam{Type: "string"},
				},
			},
			Required: []string{"question"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			question := strings.TrimSpace(stringArg(args, "question"))
			if question == "" {
				return "", Error("question is required: the text the user should answer")
			}
			q.mu.Lock()
			q.question = question
			q.options = formStepOptions(args)
			q.mu.Unlock()
			return "Sent. This run ends here; the answer arrives as the next message in this thread.", nil
		},
	}
}

// markAsDelegatedMayAsk is markAsDelegated for a run whose answer can come
// back: the same "Brief: " tail (lastDispatchTopic reads it), with ask_user
// offered for what only the user can settle instead of "make defaults".
func markAsDelegatedMayAsk(msg string) string {
	return "[DELEGATED INVOCATION] Another agent sent this brief; the user is not watching this run. Build from it, deciding anything you reasonably can yourself. " +
		"If something only the user can settle blocks you (which account or instance, a choice between meaningfully different designs), call ask_user: " +
		"this run ends, the question goes to the agent that asked, and the user's answer comes back as the next message here.\n\nBrief: " + msg
}
