package orchestrate

// A request approved or denied on the Requests tab goes in the activity log,
// naming the kind of request and the agent, never what the request said: that
// is the user's conversation, and the log is read by administrators.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/activity"
)

func TestADecidedRequestIsLoggedWithoutItsWords(t *testing.T) {
	app, req, _ := authedApp(t)
	pinRootDB(t)
	held := SaveAuthorization(RootDB, Authorization{Owner: "alice", Agent: "Scout", Brief: "private words about the song", FromAgent: "a-wiwee"})
	app.handleApprovalDeny(httptest.NewRecorder(), req(http.MethodPost, "/api/console/approvals/deny?id="+held.ID, nil))
	events := activity.List(AuthDB(), activity.Filter{Kind: activity.Approval})
	if len(events) != 1 || events[0].User != "alice" || events[0].Detail != "denied a handoff request for Scout" {
		t.Fatalf("the denial should be logged: %+v", events)
	}
	if strings.Contains(events[0].Detail, "private words") {
		t.Fatal("the request's own words reached the log")
	}
	if got := approvalActivity(Authorization{Action: "change_approval", Agent: "Builder"}, "approved"); got != "approved a change approval request for Builder" {
		t.Errorf("a kind of request reads %q", got)
	}
}
