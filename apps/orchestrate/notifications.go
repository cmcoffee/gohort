// Notifications — the owner-facing record of what an agent needed to say.
//
// core/notices holds them; this file decides who is told, and whether the
// telling also goes out to a phone or an inbox. The split is the usual one: the
// store knows what was said and how often, the app knows what a phantom bridge
// is.
//
// The FRAMEWORK writes these, not only the agent. The case that matters most is
// a tool refused on an unattended run, and that is precisely the case an agent
// is least likely to report: a model frequently does not register that it was
// refused, it narrates around the gap and finishes the turn looking successful.
// The gate knows. So the gate writes.
//
// See docs/task-notes.md's sibling reasoning about surfaces that only the model
// can see: a thing nobody can find is a thing that did not happen.

package orchestrate

import (
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/notices"
)

// notify records a notice for an owner and forwards it if they asked for that.
//
// Forwarding happens on the FIRST occurrence only. The twenty-fourth identical
// alert is not more informative than the first; it is the one that makes
// somebody turn forwarding off, and then it is off when something new happens.
// The count on the row carries the rest.
func (T *OrchestrateApp) notify(owner string, n notices.Notice) {
	n.Owner = strings.TrimSpace(owner)
	if n.Owner == "" || RootDB == nil {
		return
	}
	stored, first := notices.Record(RootDB, n)
	if !first {
		return
	}
	T.forwardNotice(stored)
}

// forwardNotice sends one notice out over whichever transports the owner chose.
//
// Both are best-effort and neither can fail the thing that produced the notice:
// the notice is already stored, and a bridge being down is not a reason for a
// scheduled run to report an error it did not have.
func (T *OrchestrateApp) forwardNotice(n notices.Notice) {
	body := n.Title
	if strings.TrimSpace(n.Body) != "" {
		body += "\n\n" + n.Body
	}
	forwardNoticeTo(n.Owner, n.Title, body)
}

// forwardNoticeTo is registered as core's NoticeForwarder, so a notice written
// by ANY app reaches the owner the same way. Core owns the store, the bell and
// the preference; this owns the transports, because knowing what a phantom
// bridge is does not belong in the hub.
func forwardNoticeTo(owner, subject, body string) {
	// AuthDB is a hook, and a hook can be unset: during boot, in a test, in any
	// host that wires the runtime without the auth surface. Forwarding is the
	// optional half of this feature and must never be the reason a refusal
	// takes the process down with it.
	if AuthDB == nil {
		return
	}
	where := AuthGetNotifyForward(AuthDB(), owner)
	if where == "" {
		return
	}
	if where == "email" || where == "both" {
		// NotifyUser is a no-op unless the username is itself an address and
		// mail is configured, which is the existing contract everywhere else.
		NotifyUser(owner, ServiceName()+": "+subject, body)
	}
	if where == "phone" || where == "both" {
		link, ok := ActiveMessagingLink()
		if !ok {
			Log("[orchestrate.notify] %s wants phone forwarding but no messaging bridge is active", owner)
			return
		}
		self, ok := link.OwnerHandle(owner)
		if !ok {
			Log("[orchestrate.notify] %s wants phone forwarding but no owner handle is configured", owner)
			return
		}
		if err := link.SendToChat(owner, self, body); err != nil {
			Log("[orchestrate.notify] forwarding to %s failed: %v", owner, err)
		}
	}
}

// noticePhoneReady answers core's one question about the phone transport: can
// this user be reached by text at all. Registered as NoticePhoneReady so the
// forwarding chooser can say which options actually deliver, without core
// learning what a bridge is.
func noticePhoneReady(user string) bool {
	link, ok := ActiveMessagingLink()
	if !ok {
		return false
	}
	_, ok = link.OwnerHandle(user)
	return ok
}

// registerNoticeTransports wires this app's transports into core's hooks. Call
// once at startup.
func registerNoticeTransports() {
	NoticeForwarder = forwardNoticeTo
	NoticePhoneReady = noticePhoneReady
}

// notifyToolWithheld is the refusal that is the owner's own standing decision.
//
// Nothing is queued and nothing is waiting on them, which is exactly why it
// needs saying somewhere durable: the run reports it in a line nobody reads
// unless they already suspect something, and there is no pending badge to
// notice. A schedule quietly doing three quarters of its job is the failure
// this catches.
func (T *OrchestrateApp) notifyToolWithheld(owner, agentID, tool string) {
	T.notify(owner, notices.Notice{
		Agent: agentID,
		Kind:  notices.KindStopped,
		Title: fmt.Sprintf("%q was not run on an unattended fire", tool),
		Body: fmt.Sprintf("%s is marked never unattended for this agent, so the run stopped short of it rather than asking. "+
			"Nothing is waiting on you. If this schedule is meant to do that work, clear the mark on the agent (or on the one that owns it); "+
			"otherwise the work belongs in a conversation where you are present.", tool),
	})
}

// notifyToolQueued is the other refusal: something IS waiting on the owner.
//
// The Permissions pane already carries it and owns the badge, so this does not
// duplicate that job; it exists so the fact can leave the building. A schedule
// that fires at 5am and needs a decision is useless news at 9am if the only
// place it lives is a pane nobody opened.
func (T *OrchestrateApp) notifyToolQueued(owner, agentID, tool string) {
	T.notify(owner, notices.Notice{
		Agent: agentID,
		Kind:  notices.KindBlocked,
		Title: fmt.Sprintf("%q needs your approval to run unattended", tool),
		Body: fmt.Sprintf("An unattended run wanted %s and it dispatches through a credential set to ask before every call. "+
			"The call was refused for that fire and queued: approve it once in the Permissions pane and later fires run it.", tool),
	})
}

// recordAgentNotice files what an agent said to its owner in its own words.
//
// Kind is KindReport: no decision is implied and nothing is waiting. The title
// is the message's first line, because a notice is a row in a list and a row
// that is four paragraphs long is one nobody scans past.
//
// Stored directly rather than through notify, so it does NOT forward. The
// caller (notify_me) is already an explicit reach-out; running it through the
// forwarding preference as well would either double-send or, since that
// preference is off by default, quietly turn a tool that always texted into
// one that usually does not.
func recordAgentNotice(owner, agentID, text string) {
	text = strings.TrimSpace(text)
	if text == "" || RootDB == nil {
		return
	}
	title, body := text, ""
	if line, rest, ok := strings.Cut(text, "\n"); ok && strings.TrimSpace(rest) != "" {
		title, body = strings.TrimSpace(line), strings.TrimSpace(rest)
	}
	if r := []rune(title); len(r) > 120 {
		title, body = strings.TrimSpace(string(r[:120]))+"…", text
	}
	notices.Record(RootDB, notices.Notice{
		Owner: owner, Agent: agentID, Kind: notices.KindReport,
		Title: title, Body: body,
	})
}
