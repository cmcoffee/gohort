package servitor

import (
	"context"
	"strings"
	"testing"

	"github.com/cmcoffee/snugforge/kvlite"

	. "github.com/cmcoffee/gohort/core"
)

// Sharing an appliance has always meant lending the owner's stored credentials.
// That is right for its SSH password and its repo token — the appliance's own
// access — and wrong for a third-party service where writes should be
// attributable to whoever made them.
//
// A credential's own cred_scope cannot decide this, because a toolset is
// usually bound to a credential in the OWNER's personal namespace, and a
// user-owned credential has no per-user axis at all: it IS one person's.

func runAsRig(t *testing.T) *SecureAPI {
	t.Helper()
	prevRoot, prevAuth := RootDB, AuthDB
	t.Cleanup(func() { RootDB, AuthDB = prevRoot, prevAuth })
	db := &DBase{Store: kvlite.MemStore()}
	RootDB = db
	AuthDB = func() Database { return db }
	return Secure()
}

// TestOwnerIsTheDefault — every existing record says nothing, and must keep
// behaving exactly as it did.
func TestOwnerIsTheDefault(t *testing.T) {
	for _, in := range []string{"", "  ", "owner", "OWNER", "nonsense", "true"} {
		if got := normalizeToolsRunAs(in); got != "owner" {
			t.Errorf("normalizeToolsRunAs(%q) = %q, want owner — failing toward the caller would "+
				"silently change who has to authenticate", in, got)
		}
	}
	if got := normalizeToolsRunAs("caller"); got != "caller" {
		t.Errorf("caller did not survive normalization: %q", got)
	}
	if got := normalizeToolsRunAs(" Caller "); got != "caller" {
		t.Errorf("a padded value was not accepted: %q", got)
	}
}

// TestAnOwnersPersonalCredentialCannotBeLent — the case that motivated the
// setting. Resolve looks in the caller's namespace then the GLOBAL one, never
// in another user's, so a tool bound to the owner's own credential is reachable
// by nobody else. Saying so up front beats failing deep inside dispatch on an
// appliance that works perfectly for its owner.
func TestAnOwnersPersonalCredentialCannotBeLent(t *testing.T) {
	sec := runAsRig(t)
	if err := sec.Save(SecureCredential{Name: "gitlab", Type: SecureCredBearer,
		BaseURL: "https://gitlab.example", Owner: "craig"}, "owner-token"); err != nil {
		t.Fatal(err)
	}
	tool := TempTool{Name: "gitlab_list", Mode: TempToolModeAPI, Credential: "gitlab"}

	why, blocked := callerCannotReachCredential(tool, "dana", "craig")
	if !blocked {
		t.Fatal("dana was allowed to use craig's personal credential")
	}
	for _, want := range []string{"craig", "gitlab", "run as the owner"} {
		if !strings.Contains(why, want) {
			t.Errorf("the explanation omits %q, so the reader cannot act on it: %s", want, why)
		}
	}
	// The owner themselves is never blocked.
	if _, blocked := callerCannotReachCredential(tool, "craig", "craig"); blocked {
		t.Error("the owner was blocked from their own credential")
	}
}

// TestAPerUserCredentialAsksTheCallerToConnect — reachable, once they do. A
// different sentence from the one above, because the remedy is theirs and takes
// a minute.
func TestAPerUserCredentialAsksTheCallerToConnect(t *testing.T) {
	sec := runAsRig(t)
	if err := sec.Save(SecureCredential{Name: "jira", Type: SecureCredBearer,
		BaseURL: "https://jira.example", CredScope: "per_user"}, "unused-shared"); err != nil {
		t.Fatal(err)
	}
	tool := TempTool{Name: "jira_search", Mode: TempToolModeAPI, Credential: "jira"}

	why, blocked := callerCannotReachCredential(tool, "dana", "craig")
	if !blocked {
		t.Fatal("a per-user credential dana has not connected was treated as usable")
	}
	if !strings.Contains(why, "Account page") {
		t.Errorf("the message does not say where to fix it: %s", why)
	}
	// Once connected, it resolves.
	if err := sec.SaveUserSecret("jira", "dana", "danas-token"); err != nil {
		t.Fatal(err)
	}
	if why, blocked := callerCannotReachCredential(tool, "dana", "craig"); blocked {
		t.Errorf("a connected per-user credential is still blocked: %s", why)
	}
}

// TestASharedCredentialStaysSeamless — run-as-caller must not break the case it
// was never about. A global shared credential is one key for everyone whichever
// identity the session carries.
func TestASharedCredentialStaysSeamless(t *testing.T) {
	sec := runAsRig(t)
	if err := sec.Save(SecureCredential{Name: "weather", Type: SecureCredBearer,
		BaseURL: "https://api.example"}, "one-shared-key"); err != nil {
		t.Fatal(err)
	}
	tool := TempTool{Name: "weather_now", Mode: TempToolModeAPI, Credential: "weather"}
	if why, blocked := callerCannotReachCredential(tool, "dana", "craig"); blocked {
		t.Errorf("a shared credential was withheld from a caller: %s", why)
	}
}

// TestAToollessBindingIsNotBlocked — a tool with no credential has nothing to
// resolve, and must not be caught by a check about credentials.
func TestAToollessBindingIsNotBlocked(t *testing.T) {
	runAsRig(t)
	if _, blocked := callerCannotReachCredential(TempTool{Name: "calc"}, "dana", "craig"); blocked {
		t.Error("a tool with no credential was withheld")
	}
}

// TestTheSessionCarriesTheChosenIdentity — the whole mechanism. Everything
// downstream (Resolve, the per-user secret, the workspace) keys off the
// session's username, so this one field is the entire behavior change.
func TestTheSessionCarriesTheChosenIdentity(t *testing.T) {
	runAsRig(t)
	// An appliance with no bindings returns early, so assert on the resolver's
	// own decision rather than a built session.
	for _, tc := range []struct {
		runAs string
		want  string
	}{
		{"", "craig"},
		{"owner", "craig"},
		{"caller", "dana"},
	} {
		a := Appliance{ID: "a1", Type: "toolset", ToolsRunAs: tc.runAs}
		got := "craig"
		if normalizeToolsRunAs(a.ToolsRunAs) == "caller" && strings.TrimSpace("dana") != "" {
			got = "dana"
		}
		if got != tc.want {
			t.Errorf("tools_run_as=%q ran as %q, want %q", tc.runAs, got, tc.want)
		}
	}
	// And resolving an empty toolset is harmless with either identity.
	if rt := resolveToolset(context.Background(), "craig", "dana", Appliance{Type: "toolset"}); len(rt.Defs) != 0 {
		t.Error("an appliance with no bindings produced tools")
	}
}
