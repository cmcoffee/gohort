package temptool

// A nested path could not be expressed at all for an API that wants the
// whole path as ONE segment. GitLab's files endpoint found it: the
// default keeps "/" raw, so a caller reaches for a pre-encoded "%2F" and
// the escaper turns the "%" into "%25". Both values 404, and the tool's
// own description suggests both.

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func TestURLEncodedModifierMakesANestedPathOneSegment(t *testing.T) {
	params := map[string]ToolParam{
		"path": {Type: "string"},
		"ref":  {Type: "string"},
	}
	args := map[string]any{"path": "src/handlers/webhook_retry.py", "ref": "dev"}

	got, err := substituteURL("/projects/42/repository/files/{path:encoded}/raw?ref={ref}", params, nil, args)
	if err != nil {
		t.Fatalf("substitute: %v", err)
	}
	const want = "/projects/42/repository/files/src%2Fhandlers%2Fwebhook_retry.py/raw?ref=dev"
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	// "segment" is the same thing: an author reaching for this is
	// guessing, and both guesses are reasonable.
	alt, err := substituteURL("/files/{path:segment}", params, nil, args)
	if err != nil || !strings.Contains(alt, "src%2Fhandlers") {
		t.Errorf("segment should be a synonym for encoded: %q (%v)", alt, err)
	}

	// The default is unchanged, because a CalDAV calendar path and a
	// /repos/owner/name must substitute as real separators.
	plain, err := substituteURL("/dav/{path}", params, nil, args)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plain, "src/handlers/webhook_retry") {
		t.Errorf("an unmodified path placeholder must keep its slashes: %q", plain)
	}
}

// The trap that produced %252F: passing a value that is ALREADY encoded.
// The escaper escapes "%", as it must — the fix is the modifier, not
// tolerating pre-encoded input, because a literal percent in a value is
// indistinguishable from an encoding the caller did by hand.
func TestPreEncodedValueStillDoubleEncodes(t *testing.T) {
	params := map[string]ToolParam{"path": {Type: "string"}}
	got, err := substituteURL("/files/{path:encoded}", params, nil, map[string]any{"path": "src%2Fhandlers%2Fwebhook_retry.py"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "%252F") {
		t.Errorf("expected the pre-encoded value to double-encode (it is why the modifier exists): %q", got)
	}
}

// A typo'd modifier must fail loudly. Emitted literally it becomes a 404
// from the far end, which is the hardest kind of wrong answer to trace
// back to a template.
func TestUnknownModifierIsRefusedAtBothEnds(t *testing.T) {
	params := map[string]ToolParam{"path": {Type: "string"}}
	if _, err := substituteURL("/files/{path:enc}", params, nil, map[string]any{"path": "a/b"}); err == nil {
		t.Error("dispatch accepted an unknown modifier")
	} else if !strings.Contains(err.Error(), "encoded") {
		t.Errorf("the refusal should name the valid modifier: %v", err)
	}
	// And at authoring time, which is where it is cheap.
	if err := validateTemplate("/files/{path:enc}", params); err == nil {
		t.Error("validation accepted an unknown modifier")
	}
	if err := validateTemplate("/files/{path:encoded}", params); err != nil {
		t.Errorf("validation rejected a good template: %v", err)
	}
	// Literal braces must stay tolerated — a JSON body template is
	// validated by the same function.
	if err := validateTemplate(`{"key": "value", "n": {"a": 1}}`, params); err != nil {
		t.Errorf("literal JSON braces should pass: %v", err)
	}
}

// A shell template has no URL encoding, but a modifier reaching a
// command line as literal "{path:encoded}" is worse than ignoring it.
func TestShellSubstitutionIgnoresTheModifier(t *testing.T) {
	params := map[string]ToolParam{"path": {Type: "string"}}
	got, err := substitute("cat {path:encoded}", params, map[string]any{"path": "a b.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "{path") {
		t.Errorf("the placeholder reached the command line verbatim: %q", got)
	}
	if !strings.Contains(got, "a b.txt") {
		t.Errorf("value missing, and it must still be quoted: %q", got)
	}
}

// An optional query param carrying a modifier must still drop out when
// no value is supplied.
func TestOptionalQueryWithModifierStillDrops(t *testing.T) {
	params := map[string]ToolParam{"ref": {Type: "string"}, "path": {Type: "string"}}
	got, err := substituteURL("/files/{path:encoded}/raw?ref={ref:encoded}", params, []string{"path"},
		map[string]any{"path": "a/b.py"})
	if err != nil {
		t.Fatalf("an omitted optional should not error: %v", err)
	}
	if strings.Contains(got, "ref=") {
		t.Errorf("the unprovided optional query param should have dropped: %q", got)
	}
}

// An update that patches command_template on an API tool must land.
// url_template and command_template are one stored field under two
// names, and the round-trip seeds BOTH — so passing one left the stale
// twin beside it and api-mode create read the twin. The update reported
// success, echoed the new value, and persisted the old one, which reads
// as a broken write path rather than a merge preferring the wrong key.
func TestUpdateAliasedTemplateFieldsAgree(t *testing.T) {
	existing := TempTool{
		Name: "repo_read_file", Mode: TempToolModeAPI, Credential: "repo_api",
		Description:     "Read a file.",
		CommandTemplate: "/api/v4/projects/{id}/repository/files/{file_path}?ref={ref}",
		Params:          map[string]ToolParam{"id": {Type: "string"}, "file_path": {Type: "string"}, "ref": {Type: "string"}},
	}
	const want = "/api/v4/projects/{id}/repository/files/{file_path:encoded}/raw?ref={ref}"

	// The round trip seeds both keys from the one stored field — that is
	// the setup for the bug, so pin it.
	seeded := tempToolToCreateArgs(existing)
	if seeded["url_template"] != existing.CommandTemplate || seeded["command_template"] != existing.CommandTemplate {
		t.Fatalf("round trip should seed both spellings: %v / %v", seeded["url_template"], seeded["command_template"])
	}

	// Patch via command_template only: url_template must follow.
	merged := tempToolToCreateArgs(existing)
	reconcileTemplateAliases(merged, existing, map[string]any{"command_template": want})
	if merged["url_template"] != want {
		t.Errorf("url_template kept the stale value %q — api-mode create reads THIS key", merged["url_template"])
	}
	if merged["command_template"] != want {
		t.Errorf("command_template did not take: %v", merged["command_template"])
	}

	// And the reverse spelling, for a shell tool patched via url_template.
	shell := TempTool{Name: "x", Mode: TempToolModeShell, CommandTemplate: "old.py {a}"}
	m2 := tempToolToCreateArgs(shell)
	reconcileTemplateAliases(m2, shell, map[string]any{"url_template": "new.py {a}"})
	if m2["command_template"] != "new.py {a}" {
		t.Errorf("shell tool's command_template kept the stale value: %v", m2["command_template"])
	}

	// Both supplied and different: the mode decides.
	m3 := tempToolToCreateArgs(existing)
	reconcileTemplateAliases(m3, existing, map[string]any{"url_template": "/api-wins", "command_template": "/cmd-loses"})
	if m3["url_template"] != "/api-wins" || m3["command_template"] != "/api-wins" {
		t.Errorf("for an api tool url_template should win: %v", m3)
	}
	m4 := tempToolToCreateArgs(shell)
	reconcileTemplateAliases(m4, shell, map[string]any{"url_template": "/url-loses", "command_template": "cmd-wins"})
	if m4["command_template"] != "cmd-wins" {
		t.Errorf("for a shell tool command_template should win: %v", m4)
	}
}

// Every "is this param wired in?" check was an exact match for "{name}",
// so {name:encoded} made them believe the param was referenced nowhere.
// The tool worked and the VALIDATOR failed it, printing "live GET
// returned 200" and "required param appears in neither template" in the
// same report. A checker that contradicts itself is worse than one that
// says nothing.
func TestModifiedPlaceholderCountsAsAReference(t *testing.T) {
	const url = "/api/v4/projects/{id}/repository/files/{file_path:encoded}/raw?ref={ref}"
	for _, p := range []string{"id", "file_path", "ref"} {
		if !templateReferences(url, p) {
			t.Errorf("%q reads as unreferenced in %s", p, url)
		}
	}
	if templateReferences(url, "nope") {
		t.Error("a param that is genuinely absent must still read as absent")
	}
	// A prefix must not count: {file_path_extra} is a different param.
	if templateReferences("/x/{file_path_extra}", "file_path") {
		t.Error("a longer param name matched as if it were this one")
	}

	// The write-path gate agrees, so a POST with a modified placeholder is
	// not rejected at authoring time.
	if unsent := unsentWriteParams("POST", url, "", []string{"file_path"}); len(unsent) > 0 {
		t.Errorf("write gate says %v is sent nowhere, but it is in the URL", unsent)
	}
	// And body scaffolding does not duplicate a param already in the path.
	body := writeBodyParams(url, map[string]ToolParam{"file_path": {Type: "string"}, "content": {Type: "string"}})
	for _, p := range body {
		if p == "file_path" {
			t.Error("a path param was scaffolded into the body as well")
		}
	}

	// The path-placeholder regex must see it too — that check exists to
	// stop a path param being declared optional, which dies at dispatch.
	if got := pathPlaceholderParams(url, []string{"id", "ref"}); len(got) != 1 || got[0] != "file_path" {
		t.Errorf("an OPTIONAL modified path placeholder should be caught, got %v", got)
	}
}
