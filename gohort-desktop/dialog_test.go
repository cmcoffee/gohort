package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A page's confirm() is answered by a native dialog, asked for synchronously
// so the answer reaches the line that called it; with no desktop app behind
// the proxy the request says so and the shim falls back.
func TestAPagesConfirmIsAskedNatively(t *testing.T) {
	js := popup_shim_js
	if !strings.Contains(js, "x.open('POST','/__desktop/dialog',false)") {
		t.Error("the shim does not ask the desktop synchronously for a dialog")
	}
	for _, kind := range []string{"'confirm'", "'alert'"} {
		if !strings.Contains(js, "__desktop_native_dialog("+kind) {
			t.Errorf("window.%s does not go through the native dialog", strings.Trim(kind, "'"))
		}
	}
	// An app's page is an inline frame the browser builds from the page's
	// own HTML, which the proxy never serves, so the parent hands its
	// dialogs into every frame it can reach, present or added later.
	for _, want := range []string{"__desktop_dialog_install(f.contentWindow)", "querySelectorAll('iframe')", "new MutationObserver("} {
		if !strings.Contains(js, want) {
			t.Errorf("the shim does not reach a page's frames: missing %s", want)
		}
	}
	gp := &gohort_proxy{}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, DIALOG_PATH, strings.NewReader(`{"kind":"confirm","message":"Delete?"}`))
	gp.ServeHTTP(w, r)
	if !strings.Contains(w.Body.String(), `"ok":false`) {
		t.Errorf("with no app, the dialog answered: %s", w.Body.String())
	}
}

// The desktop's own card stands in for the framework's confirm, alert and
// prompt, so it has to wear the PAGE's theme: it used to be painted in a fixed
// dark-grey palette with the system font, and read as a popup from some other
// product on an indigo page. Each colour is read from the page's tokens at
// open time, with the old value only as a fallback.
func TestTheDesktopModalWearsThePageTheme(t *testing.T) {
	js := popup_shim_js
	a := strings.Index(js, "function __desktop_modal_open(")
	b := strings.Index(js, "window.__uiConfirmImpl=")
	if a < 0 || b < a {
		t.Fatal("could not find the modal in the shim")
	}
	modal := js[a:b]
	for _, want := range []string{"getPropertyValue(n)", "tk('--bg-1'", "tk('--text'", "tk('--border'", "tk('--accent'", "tk('--modal-scrim'", "style.font='inherit'"} {
		if !strings.Contains(modal, want) {
			t.Errorf("the modal does not read the page theme: missing %s", want)
		}
	}
	for _, fixed := range []string{"='#161b22'", "='#e6edf3'", "'13px -apple-system", "'14px -apple-system"} {
		if strings.Contains(modal, fixed) {
			t.Errorf("the modal still paints a fixed value: %s", fixed)
		}
	}
}
