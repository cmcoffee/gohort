package ui

import (
	"strings"
	"testing"
)

// An isolated document asks the page for its dialogs: uiConfirm, uiAlert and
// uiPrompt inside the frame post to the page, the page shows its own modal
// and answers with what was chosen. Source-scan, as the other runtime tests
// are: what this pins is both halves of the relay still speaking.
func TestAnIsolatedDocumentUsesThePagesOwnDialogs(t *testing.T) {
	src := mustRuntimePart(t, "70_misc.js")
	for _, want := range []string{
		`window.uiConfirm=function(m){return dlg("confirm",m);}`,
		`window.uiPrompt=function(m,d){return dlg("prompt",m,d);}`,
		`window.alert=function(m){dlg("alert",m);}`,
		`__uiIsoDialog:1`,
		`if (d.__uiIsoDialog) {`,
		`dialog: 1, value: v === undefined ? null : v`,
		`if(d.dialog){c.res(d.value);return;}`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("the dialog relay lost a half: %s", want)
		}
	}
}
