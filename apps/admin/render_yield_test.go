package admin

import (
	"encoding/json"
	"strings"
	"testing"
)

// The scheduler panel offers the render switch beside the model's.
func TestTheSchedulerPanelOffersTheRenderSwitch(t *testing.T) {
	a := &AdminApp{}
	b, _ := json.Marshal(a.llmSections())
	for _, want := range []string{`"image_render_yield"`, "Scheduled image renders wait for people", "first come, first served"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("the LLM page is missing %q", want)
		}
	}
}
