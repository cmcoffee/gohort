package core

import "testing"

// The desktop bridge publishes its tools straight into the LLM catalog, so the
// names it mints have to survive the same check llm.go applies to every catalog
// it builds. They did not: the prefix was "from_client." and a dot is outside
// the accepted class, so dropInvalidlyNamedTools removed the ENTIRE desktop
// surface from every request — silently, since dropping is per-tool and the
// rest of the catalog still worked.
func TestClientToolNamesSurviveTheLLMNameGuard(t *testing.T) {
	// Left: what a desktop announces. Right: what the model must see.
	for raw, want := range map[string]string{
		"contacts_lookup":            "from_client_contacts_lookup",
		"filesystem_read_local_file": "from_client_filesystem_read_local_file",
		// Legacy dotted announces (a desktop that hasn't been rebuilt) have to
		// keep working — the server sanitizes, it doesn't require an upgrade.
		"contacts.lookup":            "from_client_contacts_lookup",
		"filesystem.read_local_file": "from_client_filesystem_read_local_file",
	} {
		got := ClientToolName(raw)
		if got != want {
			t.Errorf("ClientToolName(%q) = %q, want %q", raw, got, want)
		}
		if !validLLMToolName(got) {
			t.Errorf("ClientToolName(%q) = %q, which the model APIs reject", raw, got)
		}
	}
}

// A name that sanitizes to nothing can't be addressed by the model, so it must
// not be offered at all rather than published as a bare prefix.
func TestClientToolNameRejectsUnusableInput(t *testing.T) {
	for _, raw := range []string{"", "   ", "...", "///"} {
		if got := ClientToolName(raw); got != "" {
			t.Errorf("ClientToolName(%q) = %q, want \"\"", raw, got)
		}
	}
}

// Agent allow-lists persisted under the dotted spelling are still on disk. The
// gates that ask "is this a client-bridge tool?" have to recognize both, or a
// saved entry stops resolving and self-heal strips it.
func TestIsClientToolNameAcceptsBothSpellings(t *testing.T) {
	for _, name := range []string{
		"from_client_contacts_lookup",
		"from_client.contacts.lookup",
	} {
		if !IsClientToolName(name) {
			t.Errorf("IsClientToolName(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"", "fetch_url", "from_clientele_tool"} {
		if IsClientToolName(name) {
			t.Errorf("IsClientToolName(%q) = true, want false", name)
		}
	}
}

// A desktop that upgrades announces "contacts_lookup" while the persisted set
// still holds "contacts.lookup" from its last run. Both sanitize to one exposed
// name, and the catalog must carry it ONCE — a duplicate name in the tool list
// is its own provider error.
func TestLocalToolsForUserDedupsAcrossTheRename(t *testing.T) {
	const user = "dedup-test-user"
	live := &desktopClient{user: user, tools: []DesktopToolDescriptor{
		{Name: "contacts_lookup", Desc: "new spelling"},
	}}
	stale := &desktopClient{user: user, tools: []DesktopToolDescriptor{
		{Name: "contacts.lookup", Desc: "old spelling"},
	}}
	desktopReg.add(stale)
	desktopReg.add(live) // newest-connected wins
	defer func() { desktopReg.remove(live); desktopReg.remove(stale) }()

	tools := LocalToolsForUser(user)
	if len(tools) != 1 {
		names := make([]string, 0, len(tools))
		for _, tl := range tools {
			names = append(names, tl.Name())
		}
		t.Fatalf("got %d tools %v, want 1", len(tools), names)
	}
	if got := tools[0].Name(); got != "from_client_contacts_lookup" {
		t.Errorf("exposed name = %q, want from_client_contacts_lookup", got)
	}
}
