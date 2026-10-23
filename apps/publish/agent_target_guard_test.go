package publish

import (
	"context"
	"errors"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/docs"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A target whose agent publishes keeps where it put the document, so the next
// publish updates that page; an update has the agent read the page first,
// changing nothing, so an edit made there is not overwritten; and the page can
// be read back, so the next update has something to compare with.
// An agent's run returned only what it said, so every republish was a fresh
// publish with no check before it.
func TestAnAgentTargetUpdatesInPlaceAndChecksFirst(t *testing.T) {
	app := &PublishApp{}
	app.DB = &DBase{Store: kvlite.MemStore()}
	tgt, err := normalizeTarget(Target{Label: "Wiki", Uses: "agent", Agent: "a1", Instructions: "Put it in the team wiki."})
	if err != nil {
		t.Fatal(err)
	}
	tgt.ID = "w1"
	app.targetsDB("alice").Set(targetTable, tgt.ID, tgt)
	page := "# Runbook\n\nRestart the collector."
	var asked []string
	docs.RegisterAgentPublisher(func(_ context.Context, user, agent, instruction string) (string, error) {
		asked = append(asked, instruction)
		if strings.Contains(instruction, "Do NOT change anything") {
			return "Here it is.\n" + agentReadOpen + "\n" + page + "\n" + agentReadClose, nil
		}
		return "Published at https://wiki.example/pages/42.", nil
	})
	t.Cleanup(func() { docs.RegisterAgentPublisher(nil) })
	d := &targetsDest{app: app}
	req := docs.PublishRequest{Target: "w1", Title: "Runbook", Doc: docs.PublishDoc{Title: "Runbook", Markdown: page}}

	res, err := d.Publish(context.Background(), "alice", req)
	if err != nil {
		t.Fatal(err)
	}
	if res.ExternalID != "https://wiki.example/pages/42" {
		t.Errorf("the address the agent gave should be kept for the next update, got %q", res.ExternalID)
	}
	// Read back, as a background check or a publish's read-back does.
	cur, err := d.ReadPublished(context.Background(), "alice", docs.PublishRecord{Kind: "target:w1", Title: "Runbook", URL: res.ExternalID})
	if err != nil || cur.Body != page {
		t.Errorf("an agent target's copy should read back word for word, got %q %v", cur.Body, err)
	}

	// The update: read first; an edit there stops it before the agent writes.
	req.ExternalID = res.ExternalID
	asked = nil
	stop := errors.New("edited there")
	guarded := docs.WithPublishGuard(context.Background(), func(cur docs.PublishedCopy) error {
		if cur.Body != page {
			t.Errorf("the guard should see the page as it stands, got %q", cur.Body)
		}
		return stop
	})
	if _, err := d.Publish(guarded, "alice", req); !errors.Is(err, stop) {
		t.Fatalf("a guard's refusal must stop the update, got %v", err)
	}
	for _, a := range asked {
		if !strings.Contains(a, "Do NOT change anything") {
			t.Errorf("the agent was asked to write after the guard refused: %q", a)
		}
	}
	if !strings.Contains(targetInstruction(tgt, req), "change nothing and say so and why") {
		t.Error("an agent updating in place is told what to do when it cannot, in its own terms")
	}
}

// What an agent says about a page is not the page: only text between the
// markers counts, and a link is the first address in what it said.
func TestReadingTheAgentsReply(t *testing.T) {
	if _, ok := betweenMarkers("I could not open it.", agentReadOpen, agentReadClose); ok {
		t.Error("a reply without markers is not a page")
	}
	if got, ok := betweenMarkers("x "+agentReadOpen+"\n body \n"+agentReadClose+" y", agentReadOpen, agentReadClose); !ok || got != "body" {
		t.Errorf("got %q %v", got, ok)
	}
	if got := firstLink("Done: see (https://wiki.example/p/1)."); got != "https://wiki.example/p/1" {
		t.Errorf("got %q", got)
	}
	if got := firstLink("Done."); got != "" {
		t.Errorf("no link, got %q", got)
	}
}

// A destination's refusal keeps its status, so a caller can say what one
// particular refusal means.
func TestARefusalKeepsItsStatus(t *testing.T) {
	err := error(httpError{status: 409, msg: "x returned HTTP 409: version"})
	if httpStatus(err) != 409 || httpStatus(errors.New("plain")) != 0 {
		t.Error("httpStatus should read a refusal's status and nothing else")
	}
}
