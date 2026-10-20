package docs

import (
	"context"
	"strings"
	"testing"
)

// readerDest can read a page by its address and does not resolve links.
type readerDest struct{}

func (readerDest) Kind() string                    { return "readertest" }
func (readerDest) Label() string                   { return "Reader test" }
func (readerDest) Available(string) (bool, string) { return true, "" }
func (readerDest) Targets(context.Context, string) ([]PublishTarget, error) {
	return nil, nil
}
func (readerDest) Publish(context.Context, string, PublishRequest) (PublishResult, error) {
	return PublishResult{}, nil
}
func (readerDest) ReadPublished(_ context.Context, _ string, rec PublishRecord) (PublishedCopy, error) {
	return PublishedCopy{Body: "<h2>Restart</h2><p>Read from " + rec.ExternalID + "</p>"}, nil
}

// A destination that only reads is linked by the address as given, which is
// what its own publishes record, and the page comes in as Markdown.
func TestAPageIsLinkedByItsAddressWhenTheDestinationOnlyReads(t *testing.T) {
	RegisterPublishDestination(readerDest{})
	lp, err := LinkPage(context.Background(), "alice", "readertest", "t1", " https://wiki.example.com/p/9 ")
	if err != nil {
		t.Fatal(err)
	}
	if lp.Record.ExternalID != "https://wiki.example.com/p/9" || lp.Record.Target != "t1" || lp.Record.Kind != "readertest" {
		t.Errorf("record %+v", lp.Record)
	}
	if !strings.Contains(lp.Markdown, "## Restart") || !strings.Contains(lp.Markdown, "Read from https://wiki.example.com/p/9") {
		t.Errorf("markdown %q", lp.Markdown)
	}
	if !CanLinkPages("readertest") || CanLinkPages("nosuchkind") {
		t.Error("CanLinkPages read wrong")
	}
	if _, err := LinkPage(context.Background(), "alice", "readertest", "", "  "); err == nil {
		t.Error("an empty address was taken")
	}
}
