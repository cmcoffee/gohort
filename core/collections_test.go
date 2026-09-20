package core

// What keeps a collection user-scoped is not a check anybody wrote; it is that
// no handler reads a scope off a request. See Collection.Scope.

import (
	"os"
	"strings"
	"testing"
)

// The comment on Collection.Scope used to say deployment scope was
// "admin-authored only at the HTTP layer". There was no such endpoint: it
// described a gate that had never been built, which is worse than describing
// none, because the next person to add a write path reads it and believes the
// check lives somewhere else.
//
// What actually holds it shut is that nothing reads a scope off a request. This
// pins that, so the claim and the code cannot drift apart again.
func TestNothingLetsAUserMintADeploymentCollection(t *testing.T) {
	for _, f := range []string{"../apps/orchestrate/collections.go"} {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("reading %s: %v", f, err)
		}
		src := string(raw)
		// A create/update handler that started taking a scope from the caller
		// is the whole risk, and it would look innocuous in a diff.
		for _, leak := range []string{`body.Scope`, `Get("scope")`, `"scope"`} {
			if strings.Contains(src, leak) {
				t.Errorf("%s reads a scope from the request (%s); a user could mint a deployment-wide collection", f, leak)
			}
		}
	}
	// And the one that does exist is the framework's own, not somebody's.
	if DeploymentKnowledgeCollectionID == "" {
		t.Error("the framework's own deployment collection lost its id")
	}
}
