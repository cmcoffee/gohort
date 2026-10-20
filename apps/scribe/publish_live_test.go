package scribe

import (
	"context"
	"strings"
	"testing"

	"github.com/cmcoffee/gohort/core/docs"
)

// A running publish is a Monitor row with Stop, and Keep published's reads as
// work that started on its own. A finished one is gone.
func TestARunningPublishIsOnTheMonitor(t *testing.T) {
	p := publishRun{guideID: "g-live", user: "alice", where: "Team wiki", background: true,
		req: docs.PublishRequest{Title: "Runbook"}}
	job, _ := p.start(context.Background())
	t.Cleanup(func() {
		publishJobsMu.Lock()
		delete(publishJobs, "g-live")
		publishJobsMu.Unlock()
	})
	job.activity("round 2 · Writing")
	var row *struct {
		label, status, cancel, owner string
		bg                           bool
	}
	for _, e := range publishLiveEntries() {
		if e.ID == "scribe-publish:g-live" {
			row = &struct {
				label, status, cancel, owner string
				bg                           bool
			}{e.Label, e.Status, e.CancelURL, e.Owner, e.Background}
		}
	}
	if row == nil {
		t.Fatal("a running publish has no Monitor row")
	}
	if row.label != "Keeping Runbook published to Team wiki" || !row.bg || row.owner != "alice" ||
		!strings.HasPrefix(row.status, "round 2 · Writing") || row.cancel != "/scribe/publish/stop?id=g-live" {
		t.Errorf("the row reads %+v", *row)
	}
	publishJobsMu.Lock()
	job.Done = true
	publishJobsMu.Unlock()
	for _, e := range publishLiveEntries() {
		if e.ID == "scribe-publish:g-live" {
			t.Error("a finished publish is still on the Monitor")
		}
	}
}
