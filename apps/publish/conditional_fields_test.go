package publish

import (
	"net/http/httptest"
	"testing"

	"github.com/cmcoffee/oddjob/core/ui"
)

// A destination with no credential is not offered, so the settings that only
// shape one wait until its credential is named. A field that can be filled in
// and then silently ignored is worse than one that is not shown.
func TestADestinationsSettingsWaitForItsCredential(t *testing.T) {
	sec := adminSection(httptest.NewRequest("GET", "/", nil))
	form, ok := sec.Body.(ui.FormPanel)
	if !ok {
		t.Fatalf("the admin section body is %T, not a FormPanel", sec.Body)
	}
	want := map[string]string{
		"confluence_base_url": "confluence_credential",
		"webhook_label":       "webhook_credential",
		"webhook_url":         "webhook_credential",
		"webhook_format":      "webhook_credential",
	}
	seen := map[string]bool{}
	for _, f := range form.Fields {
		if gate, ok := want[f.Field]; ok {
			seen[f.Field] = true
			if f.ShowWhen != gate {
				t.Errorf("%s: ShowWhen = %q, want %q", f.Field, f.ShowWhen, gate)
			}
		}
		if f.Field == "confluence_credential" || f.Field == "webhook_credential" {
			if f.ShowWhen != "" {
				t.Errorf("%s gates the rest and must always show, got ShowWhen %q", f.Field, f.ShowWhen)
			}
		}
	}
	for f := range want {
		if !seen[f] {
			t.Errorf("the %s field is gone", f)
		}
	}
}
