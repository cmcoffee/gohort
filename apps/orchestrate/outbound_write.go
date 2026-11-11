package orchestrate

// Writes to hosts no credential covers (core/outbound_write.go), decided for
// an agent's turn.
//
// Whose decision: the agent's OWNER. A shared agent run by somebody else
// still reaches out under its owner's trusted hosts, and only the owner is
// offered "Always allow", because that answer binds every agent they own.
//
// Watched: a card, Allow once / Always allow <host> / Deny. A script cannot
// wait for one, so it is refused with what to do instead.
//
// Unwatched (a schedule, a channel, a monitor wake): refused, queued once per
// agent and host, and the owner told. Approving it trusts the host, so the
// next run goes through. No human present is neither silent success nor
// silent failure.

import (
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/notices"
	"github.com/cmcoffee/gohort/core/textutil"
)

// outboundWriteAction is the queued request a write held on an unattended run
// files: Handle is the host, Brief the call.
const outboundWriteAction = "outbound_write"

// approveWrite is ToolSession.ApproveWrite for this turn.
func (t *chatTurn) approveWrite(w OutboundWrite) string {
	w.Owner = firstNonBlank(t.agent.Owner, t.user)
	if refusal, decided := w.Decided(); decided {
		if refusal != "" {
			t.turnDiag("outbound-refused", fmt.Sprintf("A %s to %s was not sent: you blocked that host.", w.Method, w.Host))
		}
		return refusal
	}
	if t.sse != nil || t.askVia != nil {
		// Always allow binds every agent the owner has, so only the owner is
		// offered it: somebody running a shared agent can allow one call.
		refusal := w.Ask(t.askInChat, t.user == w.Owner)
		switch {
		case refusal != "" && w.Tunnel:
			t.turnDiag("outbound-refused", fmt.Sprintf("%s was refused: the site is not a package registry and you have not trusted it. To allow it, add %s under Network on the agent's Security page.", sentenceCase(w.Summary()), w.Host))
		case refusal != "" && !w.CanAsk:
			t.turnDiag("outbound-refused", fmt.Sprintf("A script's %s to %s was refused: the host is not trusted, and a script cannot stop to ask.", w.Method, w.Host))
		case refusal == "" && writeHostTrusted(w):
			t.turnDiag("outbound-trusted", fmt.Sprintf("You trusted %s for every agent you own. Take it back on the Permissions page.", w.Host))
		}
		return refusal
	}
	t.queueOutboundWrite(w)
	if w.Tunnel {
		return fmt.Sprintf("NOT CONNECTED: %s needs the owner's approval: it is not a package registry and they have not trusted it, and gohort cannot see whether a connection there reads or uploads. Nobody is watching this run, so it is waiting in their approval queue, and approving it lets later runs connect. Do not reach it another way; say what you meant to do there.", w.Summary())
	}
	return fmt.Sprintf("NOT SENT: a %s to %s needs the owner's approval: no credential covers that host and they have not trusted it. Nobody is watching this run, so it is waiting in their approval queue, and approving it lets later runs send to that host. Do not send it another way; say what you meant to send.", w.Method, w.Host)
}

// sentenceCase starts s with a capital, for a summary that opens a sentence.
func sentenceCase(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// writeHostTrusted reports whether the owner now trusts w's host.
func writeHostTrusted(w OutboundWrite) bool {
	for _, e := range ListWriteHostPolicies(RootDB, w.Owner) {
		if e.Target == w.Host {
			return e.Policy == PolicyAllow
		}
	}
	return false
}

// queueOutboundWrite files a held write for the owner, once per agent and
// host, and tells them. A second refusal while the first is still waiting
// adds nothing to the queue: the decision it asks for is the same one.
func (t *chatTurn) queueOutboundWrite(w OutboundWrite) {
	for _, a := range ListAuthorizations(RootDB, w.Owner) {
		if a.Action == outboundWriteAction && a.Agent == t.agent.ID && a.Handle == w.Host {
			Log("[outbound] %s %s: refused, already waiting for %s's approval (agent %s)", w.Method, w.Host, w.Owner, t.agent.ID)
			return
		}
	}
	SaveAuthorization(RootDB, Authorization{
		Owner:  w.Owner,
		Action: outboundWriteAction,
		Agent:  t.agent.ID,
		Handle: w.Host,
		Brief:  w.Method + " " + textutil.Clip(w.URL, 300),
	})
	Log("[outbound] %s %s: refused on an unattended run, queued for %s (agent %s)", w.Method, w.Host, w.Owner, t.agent.ID)
	if t.app != nil {
		t.app.notifyWriteQueued(w.Owner, t.agent.ID, w.Summary(), w.Host)
	}
}

// notifyWriteQueued tells the owner an unattended run was held from sending
// to a host they have not trusted.
// call is OutboundWrite.Summary, "a POST to host".
func (T *OrchestrateApp) notifyWriteQueued(owner, agentID, call, host string) {
	T.notify(owner, notices.Notice{
		Agent: agentID,
		Kind:  notices.KindBlocked,
		Title: fmt.Sprintf("An unattended run wants to send to %s", host),
		Body: fmt.Sprintf("%s was refused: no credential covers that host and you have not trusted it. "+
			"It is waiting in the Permissions pane: approving it trusts %s for every agent you own, and later runs send to it.", sentenceCase(call), host),
	})
}

// outboundWriteDisplay is the approvals-list wording for a held write.
func outboundWriteDisplay(udb Database, a Authorization) (who, detail string) {
	who = a.Agent
	if rec, found := loadAgent(udb, a.Agent); found && rec.Name != "" {
		who = rec.Name
	}
	call := strings.TrimSpace(a.Brief)
	if strings.HasPrefix(call, "CONNECT ") {
		return who, fmt.Sprintf("connect to %s through a script's proxy, a site you have not trusted, where gohort cannot see whether it reads or uploads (refused on an unattended run; approving trusts it for every agent you own)", a.Handle)
	}
	return who, fmt.Sprintf("send to %s, a host you have not trusted (refused on an unattended run; approving trusts it for every agent you own): %s", a.Handle, call)
}
