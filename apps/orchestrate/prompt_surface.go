package orchestrate

// Who reads a run's reply, and so what the framework's guidance may tell the
// model to do with it.
//
// The guidance was written for a person watching the web chat: ask with
// ask_user, approve in the Authorizations pane, lay the plan out in your
// reply before acting. It reached every surface, so a scheduled fire was told
// to ask a question nobody could answer and to write a plan nobody would
// read, while its own instruction said "Do NOT announce what you are about to
// do". The blocks keep one text; the sentences that assume a watcher are
// worded here for the surface the run is on.

import "strings"

// promptSurface is where a run's reply goes.
type promptSurface int

const (
	// surfaceWeb is a person watching the chat: the ask tools, and prose
	// written between tool calls shows as it comes.
	surfaceWeb promptSurface = iota
	// surfaceConversation is a person in a channel conversation (a text
	// thread): they read the reply and can answer it, but there is no ask
	// tool and only the reply reaches them.
	surfaceConversation
	// surfaceUnattended is nobody reading as it comes: a schedule, a wake, a
	// delegation, a sub-agent. The result is read later, if at all.
	surfaceUnattended
)

// surfaceForRun is the surface of a run on the dispatch path: a message in a
// channel conversation has a person on the other end; a wake arriving there,
// and every other run, has nobody.
func surfaceForRun(run AgentSyncRun) promptSurface {
	if run.Kind == "channel" && strings.TrimSpace(run.InputReportFrom) == "" {
		return surfaceConversation
	}
	return surfaceUnattended
}

// surfaceRewrites are the sentences of the framework blocks that assume a
// person watching the web chat, with their wording on each other surface. A
// sentence an override no longer carries is left alone: the override wins.
var surfaceRewrites = map[promptSurface][][2]string{
	surfaceConversation: {
		{"ask it yourself with ask_user / ask_user_form before building.",
			"ask it yourself, in your reply, and build once they answer."},
		{"becomes a sub-agent of yours the moment the user approves it in their Authorizations pane.",
			"becomes a sub-agent of yours the moment the user approves it."},
		{"\"I had Builder draft that for you. Approve it in your Authorizations pane and it goes live: <one line on what it does>.\"",
			"\"I had Builder draft that for you. It goes live once you approve it: <one line on what it does>.\""},
		{"Do NOT author agents / tools / pipelines / skills yourself or via plan_set.",
			"Do NOT author agents / tools / pipelines / skills yourself."},
	},
	surfaceUnattended: {
		{"ask it yourself with ask_user / ask_user_form before building.",
			"do not build on a guess: nobody is here to ask, so say in your result what is undecided and leave the build until someone can answer."},
		{"becomes a sub-agent of yours the moment the user approves it in their Authorizations pane.",
			"becomes a sub-agent of yours the moment the user approves it."},
		{"\"I had Builder draft that for you. Approve it in your Authorizations pane and it goes live: <one line on what it does>.\"",
			"\"I had Builder draft that for you. It goes live once you approve it: <one line on what it does>.\""},
		{"Do NOT author agents / tools / pipelines / skills yourself or via plan_set.",
			"Do NOT author agents / tools / pipelines / skills yourself."},
		{"When you set a monitor up, ASK the user how they want to be alerted when it fires, then set the notify field:",
			"When you set a monitor up, set the notify field (nobody is here to ask how they want to be alerted, so pick the one that fits and say which):"},
	},
}

// forSurface words a framework block for the surface its run is on.
func forSurface(text string, s promptSurface) string {
	for _, r := range surfaceRewrites[s] {
		text = strings.Replace(text, r[0], r[1], 1)
	}
	return text
}
