package orchestrate

// The privacy gate: what the worker sends the lead on the consultant and
// hybrid levels passes through here, and nothing of those levels reaches the
// lead another way. The lead is usually a hosted, billed model; the worker is
// usually on the box. A consult, a plan check and a review each carry what the
// turn saw, and the review carries an excerpt of every tool result.
//
// Three things are taken out, all by rule and none by a model's judgment (a
// judge that misses once has already leaked, and it would cost seconds on
// every lead call):
//   - every stored credential value the turn's people can reach, by value
//     (SecureAPI.RedactSecretsFor), so a key gohort holds never leaves;
//   - any line that looks like a secret (RedactSecretLines, which already
//     cleans the recall index and session exports);
//   - what a tool read from the person's own computer, shown to the reviewer
//     as kept local (keptLocalOutputs).
//
// Each lead call that loses something says how much, so it can be checked.

import (
	"context"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// leadGate is who a lead call is for: the agent, and the people whose
// credentials its turn can reach (the person in the conversation, and the
// agent's owner when that is someone else).
type leadGate struct {
	agentID string
	users   []string
}

// leadGate is this turn's gate.
func (t *chatTurn) leadGate() leadGate {
	g := leadGate{agentID: t.agent.ID, users: []string{t.user}}
	if _, owner := t.ownerView(); owner != "" && owner != t.user {
		g.users = append(g.users, owner)
	}
	return g
}

// safe is text with the credentials and secret-shaped lines taken out, and
// how many of each.
func (g leadGate) safe(text string) (string, int, int) {
	creds := 0
	for _, u := range g.users {
		var n int
		text, n = Secure().RedactSecretsFor(u, text)
		creds += n
	}
	cleaned := RedactSecretLines(text)
	lines := 0
	if cleaned != text {
		lines = strings.Count(cleaned, "[redacted: possible secret")
	}
	return cleaned, creds, lines
}

// leadChatGated is LeadChat for the consultant and hybrid levels: every
// message made safe first, and what was taken out logged.
func (T *OrchestrateApp) leadChatGated(ctx context.Context, g leadGate, purpose string, msgs []Message, opts ...ChatOption) (*Response, error) {
	out := make([]Message, len(msgs))
	creds, lines := 0, 0
	for i, m := range msgs {
		var c, l int
		m.Content, c, l = g.safe(m.Content)
		creds, lines = creds+c, lines+l
		out[i] = m
	}
	if creds+lines > 0 {
		Log("[lead-gate] agent=%s %s: %d credential value(s) and %d secret-shaped line(s) taken out before it went to the lead", g.agentID, purpose, creds, lines)
	}
	return T.LeadChat(ctx, out, opts...)
}

// keptLocal is what the reviewer is shown in place of a result a tool read
// from the person's own computer.
const keptLocal = "[kept local: read from the person's own computer]"

// keptLocalOutputs is outputs with what the desktop's tools returned replaced
// by keptLocal. Each entry is "label: excerpt", the label starting with the
// tool's name; the reviewer still sees that the call ran and whether it failed.
func keptLocalOutputs(outputs []string) []string {
	out := make([]string, len(outputs))
	for i, o := range outputs {
		name, _, _ := strings.Cut(o, ":")
		name, _, _ = strings.Cut(strings.TrimSpace(name), " ")
		if IsClientToolName(name) {
			label, _, _ := strings.Cut(o, ": ")
			o = label + ": " + keptLocal
		}
		out[i] = o
	}
	return out
}
