package orchestrate

// Asking someone a question over iMessage took two approvals: one to send,
// then a separate request_thread_binding to read the answer. message_contact
// with read_reply=true asks once, and approving it both sends and binds their
// 1:1 thread. A send that goes out without approval (a pre-authorized
// recipient) still asks once to read the reply: reading a person's replies is
// never granted silently.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// replyLink is a messaging bridge that resolves any recipient to a 1:1 chat
// and records what was delivered. Anything else would panic on the embedded
// nil interface, which keeps the test honest about what the path touches.
type replyLink struct {
	MessagingLink
	sent []string
}

func (l *replyLink) ResolveRecipient(owner, to string) (MessagingChatSummary, bool) {
	return MessagingChatSummary{ChatID: "chat-" + to, Handle: to, DisplayName: "Pat"}, true
}

func (l *replyLink) DeliverMessage(owner, chatID, handle, text string, images []string) (string, error) {
	l.sent = append(l.sent, chatID+": "+text)
	return text, nil
}

func (l *replyLink) DescribeChat(owner, chatID string) (MessagingChatSummary, bool) {
	return MessagingChatSummary{ChatID: chatID, DisplayName: "Pat"}, true
}

// replyThreads is the outbound transport sends go through; it records them on
// the link so a test reads one list.
type replyThreads struct{ l *replyLink }

func (replyThreads) Threads(string) []ChannelThreadInfo         { return nil }
func (replyThreads) Messages(string, string, int) []ChannelLine { return nil }
func (replyThreads) Members(string, string) []ChannelMember     { return nil }
func (r replyThreads) Deliver(_, _, chatID, _, text, _ string, _ []string) error {
	r.l.sent = append(r.l.sent, chatID+": "+text)
	return nil
}

func withReplyLink(t *testing.T) *replyLink {
	t.Helper()
	prev, _ := ActiveMessagingLink()
	prevThreads, had := ActiveChannelThreads()
	l := &replyLink{}
	RegisterMessagingLink(l)
	RegisterChannelThreads(replyThreads{l})
	t.Cleanup(func() {
		RegisterMessagingLink(prev)
		if had {
			RegisterChannelThreads(prevThreads)
		} else {
			RegisterChannelThreads(nil)
		}
	})
	return l
}

func messageContact(t *testing.T, sess *ToolSession, agent string, args map[string]any) string {
	t.Helper()
	for _, td := range operatorManagementTools(sess, agent) {
		if td.Tool.Name == "message_contact" {
			out, err := td.Handler(context.Background(), args)
			if err != nil {
				t.Fatal(err)
			}
			return out
		}
	}
	t.Fatal("no message_contact tool")
	return ""
}

func TestOneApprovalSendsAndBindsTheReply(t *testing.T) {
	depStores(t, "u")
	link := withReplyLink(t)
	sess := &ToolSession{Username: "u", ChatSessionID: "s-1", Network: NewNetworkConnector(true)}
	out := messageContact(t, sess, "lead", map[string]any{"to": "+15550100", "text": "Are you free Friday?", "read_reply": true})
	if !strings.Contains(out, "One approval covers both") {
		t.Fatalf("reply = %q", out)
	}
	auths := ListAuthorizations(RootDB, "u")
	if len(auths) != 1 || auths[0].Action != "send_message" || !auths[0].BindReply || auths[0].FromAgent != "lead" {
		t.Fatalf("authorizations = %+v, want one send that binds", auths)
	}
	if _, detail := approvalDisplay(nil, "u", auths[0]); !strings.Contains(detail, "read their reply") {
		t.Errorf("the approval card does not say it also reads the reply: %q", detail)
	}
	if agentBoundTo("u", "lead", "chat-+15550100", "+15550100") {
		t.Fatal("bound before anyone approved")
	}
	// Asking again for the binding on its own points at the pending one.
	for _, td := range operatorManagementTools(sess, "lead") {
		if td.Tool.Name == "request_thread_binding" {
			again, _ := td.Handler(context.Background(), map[string]any{"to": "+15550100"})
			if !strings.Contains(again, "already awaiting") {
				t.Errorf("a second binding request was queued: %q", again)
			}
		}
	}

	app := &OrchestrateApp{AppCore: AppCore{DB: orchestrateBaseDB}}
	r := httptest.NewRequest(http.MethodPost, "/api/approvals/approve?id="+auths[0].ID, nil)
	r.AddCookie(&http.Cookie{Name: "gohort_session", Value: AuthCreateSession(AuthDB(), "u")})
	w := httptest.NewRecorder()
	app.resolveApproval(w, r, false)
	if w.Code != http.StatusNoContent {
		t.Fatalf("approve answered %d %s", w.Code, w.Body.String())
	}
	if len(link.sent) != 1 || !strings.Contains(link.sent[0], "Are you free Friday?") {
		t.Errorf("sent = %v", link.sent)
	}
	if !agentBoundTo("u", "lead", "chat-+15550100", "+15550100") {
		t.Error("approving the send did not bind the reply thread")
	}
	if n := len(ListAuthorizations(RootDB, "u")); n != 0 {
		t.Errorf("%d approvals left, want none", n)
	}
}

func TestASendWithoutApprovalStillAsksOnceToReadTheReply(t *testing.T) {
	depStores(t, "u")
	link := withReplyLink(t)
	SetContactPreAuthorized(RootDB, "u", "lead", operatorRecipientKey("chat-+15550100", "+15550100"), true)
	sess := &ToolSession{Username: "u", ChatSessionID: "s-1", Network: NewNetworkConnector(true)}
	out := messageContact(t, sess, "lead", map[string]any{"to": "+15550100", "text": "Running late", "read_reply": true})
	if len(link.sent) != 1 || !strings.Contains(out, "pre-authorized") || !strings.Contains(out, "needs the user's approval") {
		t.Fatalf("sent %v, reply %q", link.sent, out)
	}
	auths := ListAuthorizations(RootDB, "u")
	if len(auths) != 1 || auths[0].Action != "bind_thread" || auths[0].Agent != "lead" {
		t.Fatalf("authorizations = %+v, want one bind_thread", auths)
	}
	if agentBoundTo("u", "lead", "chat-+15550100", "+15550100") {
		t.Fatal("a pre-authorized send bound the reply thread with nobody approving it")
	}
	// Without read_reply nothing is asked about reading.
	depStores(t, "u")
	withReplyLink(t)
	SetContactPreAuthorized(RootDB, "u", "lead", operatorRecipientKey("chat-+15550100", "+15550100"), true)
	messageContact(t, sess, "lead", map[string]any{"to": "+15550100", "text": "Running late"})
	if n := len(ListAuthorizations(RootDB, "u")); n != 0 {
		t.Errorf("%d approvals queued for a plain send to a pre-authorized recipient", n)
	}
}

// A binding is made once: approving a second request for a thread already
// bound leaves the one channel.
func TestBindingAThreadTwiceKeepsOneChannel(t *testing.T) {
	depStores(t, "u")
	bindReplyThread("u", "lead", "chat-1", "+15550100", "Pat", true)
	bindReplyThread("u", "lead", "chat-1", "+15550100", "Pat", true)
	n := 0
	for _, ch := range ListChannelsForAgent(RootDB, "u", "lead") {
		if ch.AgentBound && ch.Address == "chat-1" {
			n++
			if !ch.AutoReply || ch.Gatekeeper == "" {
				t.Errorf("a woken binding lacks its gatekeeper: %+v", ch)
			}
		}
	}
	if n != 1 {
		t.Fatalf("%d channels for one thread", n)
	}
}
