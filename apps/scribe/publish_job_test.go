package scribe

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/docs"
	"github.com/cmcoffee/snugforge/kvlite"
)

// stepDest is a destination that reports two steps and lands the page.
type stepDest struct{}

func (stepDest) Kind() string                    { return "steptest" }
func (stepDest) Label() string                   { return "Step test" }
func (stepDest) Available(string) (bool, string) { return true, "" }
func (stepDest) Targets(context.Context, string) ([]docs.PublishTarget, error) {
	return nil, nil
}
func (stepDest) Publish(ctx context.Context, user string, req docs.PublishRequest) (docs.PublishResult, error) {
	docs.PublishStep(ctx, "found the page")
	docs.PublishStep(ctx, "updated it")
	return docs.PublishResult{URL: "https://wiki.example/p/1", ExternalID: "1", Label: "Runbook"}, nil
}

// A publish job keeps what the publish did, step by step, for the dialog to
// show while it runs and after; and files where it landed on the guide.
func TestAPublishJobKeepsItsSteps(t *testing.T) {
	docs.RegisterPublishDestination(stepDest{})
	udb := UserDB(&DBase{Store: kvlite.MemStore()}, "u")
	g := saveGuide(udb, Guide{ID: "gj", Owner: "u", Title: "Runbook", Sections: []Section{{ID: "s", Title: "Install", Markdown: "Do it.", Order: 1}}})
	runPublishJob(httptest.NewRequest("POST", "/", nil), g, udb, "u", "steptest", "Step test",
		docs.PublishRequest{Title: "Runbook", Doc: docs.PublishDoc{Title: "Runbook", Markdown: "# Runbook\n\nDo it."}}, docs.PublishRecord{Kind: "steptest", Title: "Runbook"})
	deadline := time.Now().Add(5 * time.Second)
	var j publishJob
	for {
		j, _ = currentPublishJob("gj")
		if j.Done || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !j.Done || !j.OK || j.URL != "https://wiki.example/p/1" {
		t.Fatalf("job = %+v", j)
	}
	var texts []string
	for _, s := range j.Steps {
		texts = append(texts, s.Text)
	}
	if strings.Join(texts, "|") != "found the page|updated it" {
		t.Fatalf("steps = %q", texts)
	}
	saved, _ := loadGuide(udb, "gj")
	if rec, ok := docs.FindPublishRecord(saved.Published, "steptest"); !ok || rec.URL != "https://wiki.example/p/1" || rec.ExternalID != "1" {
		t.Fatalf("record = %+v, %v", rec, ok)
	}
}

// activityDest says what it is doing, then waits to be let go, so the test
// can read the job mid-run.
type activityDest struct{ release chan struct{} }

func (activityDest) Kind() string                    { return "activitytest" }
func (activityDest) Label() string                   { return "Activity test" }
func (activityDest) Available(string) (bool, string) { return true, "" }
func (activityDest) Targets(context.Context, string) ([]docs.PublishTarget, error) {
	return nil, nil
}
func (d activityDest) Publish(ctx context.Context, user string, req docs.PublishRequest) (docs.PublishResult, error) {
	docs.PublishActivity(ctx, "round 2 · Thinking · 14s")
	<-d.release
	return docs.PublishResult{URL: "https://wiki.example/p/2", ExternalID: "2"}, nil
}

// A publish job says what it is doing right now beside the spinner (its round
// and thinking), and says nothing of the sort once it has ended.
func TestAPublishJobSaysWhatItIsDoingNow(t *testing.T) {
	d := activityDest{release: make(chan struct{})}
	docs.RegisterPublishDestination(d)
	udb := UserDB(&DBase{Store: kvlite.MemStore()}, "u")
	g := saveGuide(udb, Guide{ID: "gn", Owner: "u", Title: "Runbook", Sections: []Section{{ID: "s", Title: "Install", Markdown: "Do it.", Order: 1}}})
	runPublishJob(httptest.NewRequest("POST", "/", nil), g, udb, "u", "activitytest", "Activity test",
		docs.PublishRequest{Title: "Runbook", Doc: docs.PublishDoc{Title: "Runbook", Markdown: "# Runbook\n\nDo it."}}, docs.PublishRecord{Kind: "activitytest", Title: "Runbook"})
	wait := func(ok func(publishJob) bool) publishJob {
		deadline := time.Now().Add(5 * time.Second)
		for {
			j, _ := currentPublishJob("gn")
			if ok(j) || time.Now().After(deadline) {
				return j
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	if j := wait(func(j publishJob) bool { return j.Now != "" }); j.Now != "round 2 · Thinking · 14s" || j.Done {
		t.Fatalf("mid-run the job says %q (done %v)", j.Now, j.Done)
	}
	close(d.release)
	if j := wait(func(j publishJob) bool { return j.Done }); !j.Done || j.Now != "" {
		t.Errorf("an ended job still says it is doing %q", j.Now)
	}
}
