package publish

// A destination that is an agent rather than an endpoint. The property that
// matters most is the one the registry was built on and this does not change:
// the agent picks WHERE from a handed list, and a target it made up is refused.

import (
	"context"
	"errors"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/docs"
	"github.com/cmcoffee/snugforge/kvlite"
)

func agentDestFixture(t *testing.T, dests ...AgentDestination) *PublishApp {
	t.Helper()
	app := &PublishApp{}
	app.DB = &DBase{Store: kvlite.MemStore()}
	app.saveConfig(PublishConfig{Agents: normalizeAgentDestinations(dests)})
	return app
}

// stubPublisher installs an agent runner and records what it was asked.
func stubPublisher(t *testing.T, reply string, err error) *struct {
	User, Agent, Instruction string
	Calls                    int
} {
	t.Helper()
	got := &struct {
		User, Agent, Instruction string
		Calls                    int
	}{}
	docs.RegisterAgentPublisher(func(_ context.Context, user, agent, instruction string) (string, error) {
		got.User, got.Agent, got.Instruction = user, agent, instruction
		got.Calls++
		return reply, err
	})
	t.Cleanup(func() { docs.RegisterAgentPublisher(nil) })
	return got
}

func TestAgentDestinationHandsTheDocumentToItsAgent(t *testing.T) {
	app := agentDestFixture(t, AgentDestination{
		Slug: "tickets", Label: "File a ticket", Agent: "Tickets",
		Prompt: "File {title} as a documentation task.",
	})
	got := stubPublisher(t, "Filed as DOC-412.", nil)
	d := &agentDest{app: app, slug: "tickets"}

	res, err := d.Publish(context.Background(), "alice", docs.PublishRequest{
		Title: "Rotating a key",
		Doc:   docs.PublishDoc{Title: "Rotating a key", Markdown: "Generate, publish, retire."},
	})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if got.Agent != "Tickets" {
		t.Errorf("agent = %q", got.Agent)
	}
	if got.User != "alice" {
		t.Errorf("user = %q — a publish runs as the person who asked for it", got.User)
	}
	// The destination's own phrasing, with the title substituted, and the
	// document last and whole.
	if !strings.HasPrefix(got.Instruction, "File Rotating a key as a documentation task.") {
		t.Errorf("instruction opened with %q", firstLine(got.Instruction))
	}
	if !strings.HasSuffix(got.Instruction, "Generate, publish, retire.") {
		t.Errorf("the document must come last and whole:\n%s", got.Instruction)
	}
	// What the agent said IS the account of where it went.
	if !strings.Contains(res.Label, "DOC-412") {
		t.Errorf("label = %q", res.Label)
	}
	// No external id is claimed: the framework did not see what happened, and
	// inventing one would make the next publish read as an update.
	if res.ExternalID != "" || res.Updated {
		t.Errorf("result claimed a remote identity: %+v", res)
	}
}

// The phrasing is the destination. The same document goes to two places as two
// different requests.
func TestEachAgentDestinationPhrasesTheJobItsOwnWay(t *testing.T) {
	app := agentDestFixture(t,
		AgentDestination{Slug: "tickets", Label: "Ticket", Agent: "Tickets", Prompt: "File this as a documentation task."},
		AgentDestination{Slug: "review", Label: "Review", Agent: "Reviewer", Prompt: "Open a pull request adding this page and request review."},
	)
	got := stubPublisher(t, "done", nil)
	req := docs.PublishRequest{Title: "T", Doc: docs.PublishDoc{Markdown: "body"}}

	(&agentDest{app: app, slug: "tickets"}).Publish(context.Background(), "alice", req)
	first := got.Instruction
	(&agentDest{app: app, slug: "review"}).Publish(context.Background(), "alice", req)

	if !strings.Contains(first, "documentation task") {
		t.Errorf("first instruction = %q", first)
	}
	if !strings.Contains(got.Instruction, "pull request") {
		t.Errorf("second instruction = %q", got.Instruction)
	}
	if got.Agent != "Reviewer" {
		t.Errorf("second destination reached %q", got.Agent)
	}
}

// A destination that cannot work says so while somebody is still choosing,
// not after they have picked it.
func TestAgentDestinationExplainsWhyItIsUnavailable(t *testing.T) {
	app := agentDestFixture(t, AgentDestination{Slug: "tickets", Label: "Ticket", Agent: "Tickets", Prompt: "File it."})
	d := &agentDest{app: app, slug: "tickets"}

	// No runner installed at all.
	docs.RegisterAgentPublisher(nil)
	if ok, why := d.Available("alice"); ok || !strings.Contains(why, "cannot hand a document to an agent") {
		t.Errorf("available=%v why=%q", ok, why)
	}

	stubPublisher(t, "ok", nil)
	if ok, why := d.Available("alice"); !ok {
		t.Errorf("available=false why=%q", why)
	}

	// Configured with no agent named.
	app.saveConfig(PublishConfig{Agents: []AgentDestination{{Slug: "tickets", Label: "Ticket"}}})
	if ok, why := d.Available("alice"); ok || !strings.Contains(why, "no agent is named") {
		t.Errorf("available=%v why=%q", ok, why)
	}

	// Deleted from the config entirely.
	app.saveConfig(PublishConfig{})
	if ok, why := d.Available("alice"); ok || !strings.Contains(why, "no longer configured") {
		t.Errorf("available=%v why=%q", ok, why)
	}
}

func TestAgentDestinationSurfacesItsConfiguredTargets(t *testing.T) {
	app := agentDestFixture(t, AgentDestination{
		Slug: "tickets", Label: "Ticket", Agent: "Tickets", Prompt: "File it.",
		Targets: []AgentDestinationTarget{
			{ID: "DOC", Title: "Docs queue"},
			{ID: "", Title: "dropped"},
			{ID: "ENG"},
		},
	})
	targets, err := (&agentDest{app: app, slug: "tickets"}).Targets(context.Background(), "alice")
	if err != nil {
		t.Fatalf("targets: %v", err)
	}
	if len(targets) != 2 {
		t.Fatalf("targets = %+v", targets)
	}
	if targets[0].Title != "Docs queue" || targets[0].Group != "Ticket" {
		t.Errorf("first = %+v", targets[0])
	}
	// A target with no title of its own is named by its id rather than blank.
	if targets[1].Title != "ENG" {
		t.Errorf("second = %+v", targets[1])
	}
}

func TestAgentDestinationReportsAFailedRun(t *testing.T) {
	app := agentDestFixture(t, AgentDestination{Slug: "tickets", Label: "Ticket", Agent: "Tickets", Prompt: "File it."})
	stubPublisher(t, "", errors.New("the agent is not reachable"))

	_, err := (&agentDest{app: app, slug: "tickets"}).Publish(context.Background(), "alice",
		docs.PublishRequest{Title: "T", Doc: docs.PublishDoc{Markdown: "body"}})
	if err == nil || !strings.Contains(err.Error(), "not reachable") {
		t.Fatalf("err = %v", err)
	}
}

// A slug is what already-published records point at, so a duplicate is dropped
// rather than merged or quietly renamed.
func TestNormalizeDropsBlankAndDuplicateSlugs(t *testing.T) {
	out := normalizeAgentDestinations([]AgentDestination{
		{Slug: " tickets ", Label: " Ticket ", Agent: " Tickets ", Prompt: " File it. "},
		{Slug: "tickets", Label: "Impostor", Agent: "Other"},
		{Slug: "", Label: "No slug"},
	})
	if len(out) != 1 {
		t.Fatalf("out = %+v", out)
	}
	if out[0].Slug != "tickets" || out[0].Label != "Ticket" || out[0].Agent != "Tickets" {
		t.Errorf("not trimmed: %+v", out[0])
	}
}

// The rule the registry was built on is unchanged by this destination kind: the
// grounding gate is about TARGETS, and it still refuses one that was not on the
// list the destination handed over.
func TestAnInventedTargetIsStillRefused(t *testing.T) {
	app := agentDestFixture(t, AgentDestination{
		Slug: "tickets", Label: "Ticket", Agent: "Tickets", Prompt: "File it.",
		Targets: []AgentDestinationTarget{{ID: "DOC", Title: "Docs queue"}},
	})
	stubPublisher(t, "ok", nil)
	docs.RegisterPublishDestination(&agentDest{app: app, slug: "tickets"})

	_, err := docs.PublishDocument(context.Background(), "alice", AgentKindPrefix+"tickets",
		docs.PublishRequest{Target: "MADE-UP", Title: "T", Doc: docs.PublishDoc{Markdown: "body"}})
	if err == nil {
		t.Fatal("a target the destination never offered was accepted")
	}
}
