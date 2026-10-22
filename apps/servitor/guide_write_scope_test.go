package servitor

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// An investigation adds to one of the person's existing guides only when they
// approve it: command output can steer a worker, and a guide is read as
// documentation. Where nobody can answer it is refused and pointed at
// record_finding. The tools that write say so, so a read-only run strips them.
func TestAnInvestigationAsksBeforeWritingIntoAGuide(t *testing.T) {
	unattended := &probeRun{id: "guide-write-unattended", ctx: context.Background()}
	if err := unattended.approveGuideWrite("Ops", "Cron jobs"); err == nil || !strings.Contains(err.Error(), "record_finding") {
		t.Errorf("with nobody to ask, the write must be refused toward record_finding, got %v", err)
	}
	agent := &probeRun{id: "guide-write-agent", ctx: WithActingAgent(context.Background(), "ops-agent")}
	confirmChans.Store(agent.id, pendingConfirm{ch: make(chan bool, 1), owner: "u", interactive: true})
	t.Cleanup(func() { confirmChans.Delete(agent.id) })
	if err := agent.approveGuideWrite("Ops", "Cron jobs"); err == nil {
		t.Error("an investigation an agent asked for must not write into a guide unasked")
	}

	ch := make(chan bool, 1)
	person := &probeRun{id: "guide-write-person", ctx: context.Background(), confirm: ch}
	confirmChans.Store(person.id, pendingConfirm{ch: ch, owner: "u", interactive: true})
	t.Cleanup(func() { confirmChans.Delete(person.id) })
	go func() { time.Sleep(20 * time.Millisecond); ch <- true }()
	if err := person.approveGuideWrite("Ops", "Cron jobs"); err != nil {
		t.Errorf("an approved write goes ahead: %v", err)
	}
	go func() { time.Sleep(20 * time.Millisecond); ch <- false }()
	if err := person.approveGuideWrite("Ops", "Cron jobs"); err == nil {
		t.Error("a denied write must not go ahead")
	}

	raw, err := os.ReadFile("probe_session.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	for _, want := range []string{
		"if docID != \"\" {\n\t\t\t\tif err := pr.approveGuideWrite(guide, title); err != nil {",
		"Required: []string{\"guide\", \"section_title\", \"content\"},\n\t\t\tCaps:     []Capability{CapWrite},",
		"Required: []string{\"subject\", \"body\"},\n\t\t\tCaps:     []Capability{CapWrite},",
		"Required: []string{\"name\", \"lang\", \"code\"},\n\t\t\tCaps:     []Capability{CapWrite},",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("probe_session.go lost %q", want)
		}
	}
}
