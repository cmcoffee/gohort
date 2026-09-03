package servitor

import (
	"os"
	"strings"
	"testing"
)

// A write-only secret must survive a save that never carried it.
//
// Every read path blanks Password and RepoToken before the record leaves the
// server, so the edit form always loads with them empty — which means an empty
// field on save says "you were not shown this", never "clear it".
//
// The guard used to read the INCOMING type: `req.Type == "repo" && ...`. Any
// save whose body did not carry that type wrote a blank straight over a stored
// token, and it presented as "no token configured" rather than as a token that
// had stopped working, because by then there genuinely was none. Asserted on
// the source because the handler is inline in an HTTP switch with no seam to
// call — the shape of the guard IS the fix.
func TestAWriteOnlySecretIsNotBlankedByASaveThatOmitsIt(t *testing.T) {
	raw, err := os.ReadFile("web.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)

	for _, want := range []string{
		"if req.Password == \"\" {\n\t\t\t\treq.Password = existing.Password",
		"if req.RepoToken == \"\" {\n\t\t\t\treq.RepoToken = existing.RepoToken",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("a write-only secret must be preserved on ANY save that omits it:\n%s", want)
		}
	}
	// The type-gated form is what let the blank through. If it comes back,
	// so does the bug.
	for _, bad := range []string{
		`req.Type == "ssh" && req.Password == ""`,
		`req.Type == "repo" && req.RepoToken == ""`,
	} {
		if strings.Contains(src, bad) {
			t.Errorf("the preserve is gated on the incoming type again: %s", bad)
		}
	}
	// A real type change still drops the secret that belongs to the kind this
	// appliance no longer is — it can never be used again, and leaving it is
	// secret material sitting in a record nobody thinks holds one.
	if !strings.Contains(src, `if req.Type != "" && req.Type != existing.Type {`) {
		t.Error("a genuine type change must clear the secret of the type being left")
	}
}
