package docs

import (
	"context"
	"testing"
)

type familyDest struct{ got PublishRequest }

func (d *familyDest) Kind() string                    { return "fam:" }
func (d *familyDest) Label() string                   { return "Family" }
func (d *familyDest) Available(string) (bool, string) { return true, "" }
func (d *familyDest) Targets(context.Context, string) ([]PublishTarget, error) {
	return []PublishTarget{{ID: "blog", Title: "Blog"}}, nil
}
func (d *familyDest) Publish(_ context.Context, _ string, req PublishRequest) (PublishResult, error) {
	d.got = req
	return PublishResult{URL: "https://example.test/p/1"}, nil
}
func (d *familyDest) TargetSpecs(context.Context, string) []PublishTargetSpec {
	return []PublishTargetSpec{{Kind: "fam:blog", Target: PublishTarget{ID: "blog", Title: "Blog"},
		Fields: []PublishField{{Name: "category", Label: "Category", Required: true}}}}
}

// Each target is its own kind under one registered family, so a document's
// publish record per kind tells targets apart; the answers reach the publish.
func TestATargetFamilyServesEveryKindInIt(t *testing.T) {
	d := &familyDest{}
	RegisterPublishDestination(d)
	res, err := PublishDocument(context.Background(), "u", "fam:blog", PublishRequest{Target: "blog", Title: "Launch", Answers: map[string]string{"category": "News"}})
	if err != nil || res.URL == "" || d.got.Answers["category"] != "News" {
		t.Fatalf("the family should serve fam:blog with its answers: %v %+v", err, d.got)
	}
	if _, err := PublishDocument(context.Background(), "u", "fam:blog", PublishRequest{Target: "made-up", Title: "Launch"}); err == nil {
		t.Error("a target outside the list is still refused")
	}
	specs := PublishTargetSpecs(context.Background(), "u")
	found := false
	for _, s := range specs {
		found = found || s.Kind == "fam:blog"
	}
	if !found {
		t.Errorf("the family's targets should be described: %+v", specs)
	}
	if m := MissingAnswers(specs[0].Fields, nil); len(m) != 1 || m[0] != "Category" {
		t.Errorf("a required question left empty is named: %v", m)
	}
}

// appendOnlyTarget is a writer that takes sections only one push at a time.
type appendOnlyTarget struct{ title, section, text string }

func (a *appendOnlyTarget) Kind() string          { return "append-only-test" }
func (a *appendOnlyTarget) Label() string         { return "Append only" }
func (a *appendOnlyTarget) List(string) []DocItem { return nil }
func (a *appendOnlyTarget) Append(_ context.Context, _, _, newTitle, section, md string) (string, error) {
	a.title, a.section, a.text = newTitle, section, md
	return "doc-1", nil
}

// A writer that cannot take sections gets them as one, under their headings;
// one with nothing to make is refused before it is asked.
func TestCreateDocumentFallsBackToOneSection(t *testing.T) {
	tgt := &appendOnlyTarget{}
	RegisterDocumentTarget(tgt)
	id, err := CreateDocument(context.Background(), "alice", tgt.Kind(), "Title", "brief", []DocSection{{Title: "A", Text: "one"}, {Title: "B", Text: "two"}})
	if err != nil || id != "doc-1" || tgt.title != "Title" || tgt.text != "## A\n\none\n\n## B\n\ntwo" {
		t.Errorf("fallback: %q %v %+v", id, err, tgt)
	}
	if _, err := CreateDocument(context.Background(), "alice", tgt.Kind(), "Title", "", nil); err == nil {
		t.Error("a document with no sections was made")
	}
	if _, err := CreateDocument(context.Background(), "alice", "nobody-registered", "Title", "", []DocSection{{Title: "A", Text: "x"}}); err == nil {
		t.Error("an unknown kind should be an error")
	}
}
