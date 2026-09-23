package orchestrate

import (
	"strings"
	"testing"
)

// A personal draft must not be created over a working DEPLOYMENT credential of
// the same name.
//
// Resolution tries the user's own namespace first, so the draft shadows the
// global one: every tool naming it starts resolving to a disabled record with
// no secret, while the working credential sits there untouched and
// unreachable. It reads exactly like the credential losing its secret, which
// is how it was found.
//
// The existing guard checked only the user's OWN record, on the reasoning that
// SaveAPIDraft keys by owner so nothing else can be overwritten. True about
// overwriting, wrong about breaking.
func TestADraftDoesNotShadowAWorkingDeploymentCredential(t *testing.T) {
	src := mustReadFile(t, "builder_tools.go")
	if !strings.Contains(src, "if _, globalExists := Secure().Load(credName); globalExists {") {
		t.Error("draft_api_credential does not check for a working global credential of the same name")
	}
	// Refused on the same terms as the own-record guard: in use, or holding a
	// secret. An unfinished global draft stays draftable.
	if !strings.Contains(src, "CredentialStatus(credName); enabled || hasSecret") {
		t.Error("the global check does not test for a credential that is actually in use")
	}
	// And it says what to do instead, because a refusal an agent cannot act on
	// is one it routes around.
	for _, want := range []string{"would SHADOW it", "pass credential=", "a different name"} {
		if !strings.Contains(src, want) {
			t.Errorf("the refusal is missing %q", want)
		}
	}
}
