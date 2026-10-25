package filestore

import (
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// A two-phase response is one whole argv element of the admin's binary, so a
// response starting with '-' reached it as an option rather than a value.
func TestCommandInputCannotBeAnOption(t *testing.T) {
	app, st, argvLog, _ := actionFixture(t, true)
	r := httptest.NewRequest("POST", "/api/commands/run?slug="+st.Slug+"&within=bundle-1&command=decrypt",
		strings.NewReader(`{"input":"--output=/tmp/elsewhere"}`))
	w := httptest.NewRecorder()
	routed(app)(w, asAdmin(t, r, "user1"))
	if w.Code != 400 {
		t.Fatalf("a leading-dash response should be refused, got %d %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(argvLog); err == nil {
		t.Fatal("the command ran with an option-shaped response")
	}
}
