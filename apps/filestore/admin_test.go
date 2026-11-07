package filestore

import "testing"

// A command's guide is Markdown agents and Builder read, edited as a document
// (Markdown or Raw).
func TestACommandsGuideIsAMarkdownDocument(t *testing.T) {
	for _, f := range commandUsesFields() {
		if f.Field == "guide" {
			if f.Type != "sections" || !f.SectionsAllowFree {
				t.Fatalf("the guide is a %q field (free sections %v)", f.Type, f.SectionsAllowFree)
			}
			return
		}
	}
	t.Fatal("no guide field")
}
