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
	args := map[string]any{"path": "lib/python/sw_update/sw_update_lib.py", "ref": "dev"}

	got, err := substituteURL("/projects/160/repository/files/{path:encoded}/raw?ref={ref}", params, nil, args)
	if err != nil {
		t.Fatalf("substitute: %v", err)
	}
	const want = "/projects/160/repository/files/lib%2Fpython%2Fsw_update%2Fsw_update_lib.py/raw?ref=dev"
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	// "segment" is the same thing: an author reaching for this is
	// guessing, and both guesses are reasonable.
	alt, err := substituteURL("/files/{path:segment}", params, nil, args)
	if err != nil || !strings.Contains(alt, "lib%2Fpython") {
		t.Errorf("segment should be a synonym for encoded: %q (%v)", alt, err)
	}

	// The default is unchanged, because a CalDAV calendar path and a
	// /repos/owner/name must substitute as real separators.
	plain, err := substituteURL("/dav/{path}", params, nil, args)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plain, "lib/python/sw_update") {
		t.Errorf("an unmodified path placeholder must keep its slashes: %q", plain)
	}
}

// The trap that produced %252F: passing a value that is ALREADY encoded.
// The escaper escapes "%", as it must — the fix is the modifier, not
// tolerating pre-encoded input, because a literal percent in a value is
// indistinguishable from an encoding the caller did by hand.
func TestPreEncodedValueStillDoubleEncodes(t *testing.T) {
	params := map[string]ToolParam{"path": {Type: "string"}}
	got, err := substituteURL("/files/{path:encoded}", params, nil, map[string]any{"path": "bin%2Fconfigmon.py"})
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
