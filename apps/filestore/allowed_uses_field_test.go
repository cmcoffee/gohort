package filestore

import "testing"

// A command's allowed uses are a rules list, one use a row, saved as the
// lines the save handler splits.
func TestAllowedUsesAreARulesList(t *testing.T) {
	for _, f := range commandUsesFields() {
		if f.Field == "allowed_uses" {
			if f.Type != "rules" {
				t.Fatalf("allowed_uses is a %q field", f.Type)
			}
			return
		}
	}
	t.Fatal("no allowed_uses field")
}
