// An investigation an agent launches, that the owner can approve.
//
// InvestigateSync is read-only: a command that needs approval is declined,
// because nobody is watching a run another app started. That keeps an agent
// from ever changing a system, and it also stops it finding out anything that
// takes one approved command to see. So when the agent's turn has a person
// watching it, the investigation runs as a session of its own in the system's
// Sessions, and a command that needs approval parks there on an ordinary card.
// The agent's call comes back at once with a link to that session for the
// person to follow; the investigation carries on in the background once they
// answer, and its findings wake the conversation that asked, the way a long
// render does. The session says who started it, and links back to that
// conversation.
//
// Where nobody can follow a link (a schedule, a messaging conversation, a
// turn the framework cannot report back into), the agent is refused at the
// first command that needs approval, with what would allow it. The same
// refusal as an agent driving a system directly gets.
package servitor

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// agentApprovalWait is how long a parked command waits for the owner: long
// enough to follow a link from another app, where the console's five minutes
// is for someone already looking at the card.
const agentApprovalWait = 30 * time.Minute

// agentInvestigationCap bounds a whole investigation once it is in the
// background, so a wedged connection cannot hold one open for good.
const agentInvestigationCap = time.Hour

// investigationOrigin is who started a session from outside servitor.
type investigationOrigin struct {
	Agent string `json:"agent,omitempty"`
	Title string `json:"title,omitempty"`
	URL   string `json:"url,omitempty"`
}

type approvalByLinkKey struct{}

// withApprovalByLink marks a run whose commands may park for the owner.
// park is told each command that is about to, and answers whether it may: a
// run that cannot hand over a link says no, and the command is refused.
func withApprovalByLink(ctx context.Context, park func(cmd string) bool) context.Context {
	return context.WithValue(ctx, approvalByLinkKey{}, park)
}

// approvalByLink is the run's park, or nil when its commands may not wait.
func approvalByLink(ctx context.Context) func(cmd string) bool {
	if ctx == nil {
		return nil
	}
	park, _ := ctx.Value(approvalByLinkKey{}).(func(string) bool)
	return park
}

// approvalByLinkPossible reports whether this turn can take a link to the
// owner and be woken with the result: a person is watching it in the web
// chat, and the framework can deliver background work back into it.
func approvalByLinkPossible(sess *ToolSession) bool {
	if sess == nil || sess.Detached || sess.Unattended() || sess.PendingApprovalPrompt == nil {
		return false
	}
	if TaskRunnerFunc == nil || ConversationLinkFunc == nil || sess.DeliverySession() == "" {
		return false
	}
	return ConversationLinkFunc(sess).Path != ""
}

// InvestigateForAgent answers an agent's question about a system. ctx bounds
// the part the agent waits for, and is the turn's: its Stop ends the run
// while the agent is still waiting. Once a command parks for the owner the
// run is the background's, and stops from the live view or servitor.
func (T *Servitor) InvestigateForAgent(ctx context.Context, sess *ToolSession, user, applianceID, question, agentID string) (string, error) {
	// Only the person the investigation runs as can answer its cards, so the
	// link is for them: someone else in a shared agent's conversation gets
	// the refusal, not a session they cannot open.
	if !approvalByLinkPossible(sess) || sess.Username != user {
		return T.InvestigateSync(WithActingAgent(ctx, agentID), user, applianceID, question)
	}
	if T == nil || T.DB == nil || user == "" || applianceID == "" {
		return "", fmt.Errorf("investigate: missing user or system")
	}
	q := strings.TrimSpace(question)
	if q == "" {
		return "", fmt.Errorf("investigate: empty question")
	}
	udb := UserDB(T.DB, user)
	appliance, ownerUser, _, found := T.resolveAppliance(user, udb, applianceID)
	if !found {
		return "", fmt.Errorf("investigate: system %q not found", applianceID)
	}
	name := applianceLabel(appliance.Name, appliance.ID)
	label := name + ": " + q
	if len(label) > 80 {
		label = label[:80]
	}
	link := ConversationLinkFunc(sess)
	origin := investigationOrigin{Agent: link.Agent, Title: link.Title, URL: link.Path}

	// A session of the system's own, so the link opens it like any other
	// and it stays in Sessions with its answer afterwards.
	sid := ensureSession(udb, appliance.ID, "", q)
	runCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), agentInvestigationCap)
	probeSessions.Register(sid, label, cancel).SetOwner(user)
	sessionAppliances.Store(sid, appliance.ID)
	confirm := make(chan bool, 1)
	confirmChans.Store(sid, pendingConfirm{ch: confirm, owner: user, interactive: true})
	RegisterInjectionQueue(sid, user, "")
	emit(sid, probeEvent{Kind: "origin", Origin: &origin})

	waiting := make(chan string, 1)
	var noLink atomic.Bool
	park := func(cmd string) bool {
		if noLink.Load() {
			return false
		}
		select {
		case waiting <- cmd:
		default:
		}
		return true
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer cancel()
		runInvestigation(T, withApprovalByLink(WithActingAgent(runCtx, agentID), park), sid, user, ownerUser, appliance, confirm,
			[]Message{{Role: "user", Content: q}}, udb)
		noteOrigin(udb, appliance.ID, sid, origin)
	}()

	select {
	case <-done:
		return investigationAnswer(sid)
	case <-ctx.Done():
		cancel()
		<-done
		return "", ctx.Err()
	case cmd := <-waiting:
		run, err := TaskRunnerFunc(sess, "investigating "+name, func(tctx context.Context) (TaskProduct, error) {
			select {
			case <-done:
			case <-tctx.Done():
				cancel()
				<-done
				return TaskProduct{}, tctx.Err()
			}
			answer, err := investigationAnswer(sid)
			if err != nil {
				return TaskProduct{}, err
			}
			return TaskProduct{Text: answer}, nil
		})
		if err != nil {
			// Nowhere to report back after all: the command waiting is
			// declined, the rest are refused, and the agent waits for what
			// the investigation finds without them.
			noLink.Store(true)
			select {
			case confirm <- false:
			default:
			}
			select {
			case <-done:
				return investigationAnswer(sid)
			case <-ctx.Done():
				cancel()
				<-done
				return "", ctx.Err()
			}
		}
		Log("[servitor] agent %s investigation on %s parked for the owner: %s", agentID, appliance.ID, cmd)
		return MarkFrameworkResult(approvalNotice(run, cmd, name, investigationPath(sid))), nil
	}
}

// runInvestigation runs one investigation's session: runSession, unless a
// test plays one.
var runInvestigation = func(T *Servitor, ctx context.Context, sid, user, ownerUser string, a Appliance, confirm chan bool, msgs []Message, udb Database) {
	T.runSession(ctx, sid, user, ownerUser, a, confirm, msgs, udb, false)
}

// investigationPath opens a session in servitor, live while it runs.
func investigationPath(sid string) string {
	return "/servitor/?reconnect=" + url.QueryEscape(sid)
}

// approvalNotice is what the agent is told when its investigation parks: not
// a result, a request to hand on. The framework's own "started" notice says
// to keep the person out of the machinery, and here the person is the next
// step.
func approvalNotice(run TaskRun, cmd, system, path string) string {
	var b strings.Builder
	b.WriteString("WAITING FOR THE OWNER, NOT FINISHED. The investigation of " + system + " wants to run a command that needs the owner's approval: `" + truncateCmd(cmd, 160) + "`.\n")
	b.WriteString("Nothing has run yet. It is waiting in Servitor, as task " + run.ID + ", and carries on once they answer; what it finds then arrives in this conversation on its own, and you will be told.\n")
	b.WriteString("Tell the person, in a line or two, what it wants to run and why it needs their OK, and give them this link to approve or deny it: [Open the investigation in Servitor](" + path + ")\n")
	b.WriteString("Do NOT describe any findings, there are none yet, and do NOT call the investigation again for this: a second call starts a second one. The approval waits " + humanizeWait(agentApprovalWait) + ".")
	return b.String()
}

func humanizeWait(d time.Duration) string {
	if d >= time.Hour {
		return fmt.Sprintf("%d hour(s)", int(d/time.Hour))
	}
	return fmt.Sprintf("%d minutes", int(d/time.Minute))
}

// investigationAnswer is the investigation's final reply, from its events.
func investigationAnswer(sid string) (string, error) {
	events, _ := probeSessions.SnapshotEvents(sid)
	var reply, errText string
	for _, ev := range events {
		switch ev.Kind {
		case "reply":
			reply = ev.Text
		case "error":
			errText = ev.Text
		}
	}
	if strings.TrimSpace(reply) == "" {
		if strings.TrimSpace(errText) != "" {
			return "", fmt.Errorf("investigate: %s", errText)
		}
		return "", fmt.Errorf("investigate: no answer produced")
	}
	return reply, nil
}

// noteOrigin adds who asked to the session's stored answer, so it says where
// it came from after the run, when the live origin line is gone.
func noteOrigin(udb Database, applianceID, sid string, o investigationOrigin) {
	if o.URL == "" {
		return
	}
	s, ok := loadSession(udb, applianceID, sid)
	if !ok {
		return
	}
	for i := len(s.Messages) - 1; i >= 0; i-- {
		if s.Messages[i].Role != "assistant" {
			continue
		}
		s.Messages[i].Content = strings.TrimRight(s.Messages[i].Content, "\n") + "\n\n" + originLine(o)
		saveSession(udb, applianceID, s)
		return
	}
}

// originLine is who asked, as markdown: the agent and a link to its
// conversation.
func originLine(o investigationOrigin) string {
	who := strings.TrimSpace(o.Agent)
	if who == "" {
		who = "an agent"
	}
	title := strings.TrimSpace(o.Title)
	if title == "" {
		title = "its conversation"
	}
	return "_Asked by " + who + " in [" + strings.ReplaceAll(title, "]", ")") + "](" + o.URL + ")._"
}
