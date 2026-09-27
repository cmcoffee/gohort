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
