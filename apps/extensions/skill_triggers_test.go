package extensions

import (
	"encoding/json"
	"strings"
	"testing"
)

// A skill's triggers arrive from the tags editor as a list, or as one pattern
// per line from a caller still sending text; either is the same list, blanks
// dropped. The form field is the tags editor.
func TestSkillTriggersTakeAListOrLines(t *testing.T) {
	var body struct {
		Triggers skillTriggers `json:"triggers"`
	}
	for _, in := range []string{`{"triggers":["contract"," *.pdf ",""]}`, `{"triggers":"contract\n *.pdf \n\n"}`} {
		body.Triggers = nil
		if err := json.Unmarshal([]byte(in), &body); err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(body.Triggers, "|"); got != "contract|*.pdf" {
			t.Errorf("%s read as %q", in, got)
		}
	}
	for _, f := range userSkillFormFields() {
		if f.Field == "triggers" && f.Type != "tags" {
			t.Errorf("the triggers field is a %q field", f.Type)
		}
	}
}
