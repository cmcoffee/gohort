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
	gp := &gohort_proxy{}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, DIALOG_PATH, strings.NewReader(`{"kind":"confirm","message":"Delete?"}`))
	gp.ServeHTTP(w, r)
	if !strings.Contains(w.Body.String(), `"ok":false`) {
		t.Errorf("with no app, the dialog answered: %s", w.Body.String())
	}
}
