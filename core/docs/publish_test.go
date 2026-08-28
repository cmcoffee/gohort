package docs

import (
	"context"
	"strings"
	"testing"
)

// fakeDest is a destination that records what it was asked to publish, so a
// test can assert on what DID and did NOT reach it.
type fakeDest struct {
	kind      string
	available bool
	reason    string
	targets   []PublishTarget
	got       []PublishRequest
}

func (f *fakeDest) Kind() string  { return f.kind }
func (f *fakeDest) Label() string { return strings.ToUpper(f.kind) }
func (f *fakeDest) Available(user string) (bool, string) {
	return f.available, f.reason
}
func (f *fakeDest) Targets(ctx context.Context, user string) ([]PublishTarget, error) {
	return f.targets, nil
}
func (f *fakeDest) Publish(ctx context.Context, user string, req PublishRequest) (PublishResult, error) {
	f.got = append(f.got, req)
	return PublishResult{ExternalID: "page-1", URL: "https://example/page-1", Version: 1}, nil
}

// reset clears the process-wide registry between cases, since it's package
// state shared by every test in this file.
func reset() {
	publishMu.Lock()
	publishDests = map[string]PublishDestination{}
	publishMu.Unlock()
}

func sampleDoc() PublishDoc {
	return PublishDoc{Title: "Getting Started", Markdown: "# Getting Started\n\nBody.", SourceKind: "guide", SourceID: "g1"}
}

// The gate that matters: a target the destination never offered must not become
// an outbound write. This is what stands between a model recalling a space key
// and a page landing in the wrong place.
func TestPublishDocumentRejectsUnlistedTarget(t *testing.T) {
	reset()
	d := &fakeDest{kind: "wiki", available: true, targets: []PublishTarget{{ID: "98304", Title: "Engineering"}}}
	RegisterPublishDestination(d)

	_, err := PublishDocument(context.Background(), "craig", "wiki", PublishRequest{
		Target: "ENG", // the KEY, not the id it was handed — a plausible-looking guess
		Title:  "Getting Started",
		Doc:    sampleDoc(),
	})
	if err == nil {
		t.Fatal("expected an unlisted target to be refused")
	}
	if len(d.got) != 0 {
		t.Fatalf("refused publish still reached the destination: %+v", d.got)
	}
	if !strings.Contains(err.Error(), "not one of") {
		t.Errorf("error should say the target wasn't on offer, got: %v", err)
	}
}

func TestPublishDocumentAcceptsListedTarget(t *testing.T) {
	reset()
	d := &fakeDest{kind: "wiki", available: true, targets: []PublishTarget{{ID: "98304", Title: "Engineering"}}}
	RegisterPublishDestination(d)

	res, err := PublishDocument(context.Background(), "craig", "wiki", PublishRequest{
		Target: "98304",
		Doc:    sampleDoc(),
	})
	if err != nil {
		t.Fatalf("publish to a listed target failed: %v", err)
	}
	if res.ExternalID != "page-1" {
		t.Errorf("external id = %q, want page-1", res.ExternalID)
	}
	if len(d.got) != 1 {
		t.Fatalf("destination saw %d publishes, want 1", len(d.got))
	}
	// An empty title falls back to the document's own, so a caller that omits
	// it doesn't create an untitled page.
	if d.got[0].Title != "Getting Started" {
		t.Errorf("title = %q, want the document's own title", d.got[0].Title)
	}
}

// A destination with no targets (a webhook is one fixed endpoint) has nothing
// to pick wrong, so it publishes without one.
func TestPublishDocumentTargetlessDestination(t *testing.T) {
	reset()
	d := &fakeDest{kind: "hook", available: true}
	RegisterPublishDestination(d)

	if _, err := PublishDocument(context.Background(), "craig", "hook", PublishRequest{Doc: sampleDoc()}); err != nil {
		t.Fatalf("targetless publish failed: %v", err)
	}
	if len(d.got) != 1 {
		t.Fatalf("destination saw %d publishes, want 1", len(d.got))
	}
}

// An unavailable destination is refused BEFORE the write, and the reason it
// gave is carried through — that reason is the user's next step.
func TestPublishDocumentRefusesUnavailable(t *testing.T) {
	reset()
	d := &fakeDest{kind: "wiki", available: false, reason: "you haven't connected your account yet"}
	RegisterPublishDestination(d)

	_, err := PublishDocument(context.Background(), "craig", "wiki", PublishRequest{Target: "x", Doc: sampleDoc()})
	if err == nil {
		t.Fatal("expected an unavailable destination to be refused")
	}
	if !strings.Contains(err.Error(), "connected your account") {
		t.Errorf("the destination's own reason should reach the caller, got: %v", err)
	}
	if len(d.got) != 0 {
		t.Fatalf("refused publish still reached the destination: %+v", d.got)
	}
}

func TestPublishDocumentUnknownKind(t *testing.T) {
	reset()
	if _, err := PublishDocument(context.Background(), "craig", "nope", PublishRequest{Doc: sampleDoc()}); err == nil {
		t.Fatal("expected an unknown destination kind to fail")
	}
}

// Unavailable destinations stay ON the list with their reason, so a user learns
// Confluence needs connecting rather than seeing it silently absent.
func TestPublishDestinationsIncludesUnavailableWithReason(t *testing.T) {
	reset()
	RegisterPublishDestination(&fakeDest{kind: "wiki", available: false, reason: "no credential is configured"})
	RegisterPublishDestination(&fakeDest{kind: "hook", available: true})

	got := PublishDestinations("craig")
	if len(got) != 2 {
		t.Fatalf("got %d destinations, want 2", len(got))
	}
	for _, d := range got {
		if d.Kind == "wiki" {
			if d.Available {
				t.Error("wiki should be unavailable")
			}
			if d.Reason != "no credential is configured" {
				t.Errorf("reason = %q, want the destination's own", d.Reason)
			}
		}
	}
}

// One document has one home per destination: publishing again replaces the
// record rather than accumulating them, which is what keeps "update the page I
// made last time" meaning one page.
func TestUpsertPublishRecordReplacesByKind(t *testing.T) {
	records := []PublishRecord{{Kind: "confluence", ExternalID: "1", Version: 1}}
	records = UpsertPublishRecord(records, PublishRecord{Kind: "confluence", ExternalID: "1", Version: 2})
	if len(records) != 1 {
		t.Fatalf("got %d records, want 1", len(records))
	}
	if records[0].Version != 2 {
		t.Errorf("version = %d, want the updated 2", records[0].Version)
	}

	records = UpsertPublishRecord(records, PublishRecord{Kind: "hook", ExternalID: "9"})
	if len(records) != 2 {
		t.Fatalf("a different destination should append: got %d records, want 2", len(records))
	}
	if _, ok := FindPublishRecord(records, "confluence"); !ok {
		t.Error("the confluence record should survive appending another destination")
	}
	if _, ok := FindPublishRecord(records, "missing"); ok {
		t.Error("FindPublishRecord should report absence for a destination never published to")
	}
}
