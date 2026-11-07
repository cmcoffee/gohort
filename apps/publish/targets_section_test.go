package publish

import "testing"

// A target's "How to publish there" is the instruction the publish pass
// follows, edited as a document (Markdown or Raw), and still required.
func TestHowToPublishThereIsAMarkdownDocument(t *testing.T) {
	for _, f := range targetFormFields("user1") {
		if f.Field == "instructions" {
			if f.Type != "sections" || !f.Required {
				t.Fatalf("instructions: type %q, required %v", f.Type, f.Required)
			}
			return
		}
	}
	t.Fatal("no instructions field")
}
