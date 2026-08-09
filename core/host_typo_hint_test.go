// The reported error printed both hostnames and was still unreadable:
//
//	upload_url host "alpaca.snuglab.locl:8188" must match
//	submit_url host "alpaca.snuglab.local:8188"
//
// One missing letter, in two strings that look identical at a glance — so the
// message read as the validator misfiring rather than as a typo, and the admin
// went looking for a caching bug instead.
package core

import (
	"strings"
	"testing"
)

func TestTheTypoIsPointedAtNotJustPrinted(t *testing.T) {
	err := sameImageHost("http://alpaca.snuglab.local:8188/prompt", "http://alpaca.snuglab.locl:8188/upload/image")
	if err == nil {
		t.Fatal("mismatched hosts must still be an error")
	}
	msg := err.Error()

	// Both hosts still named — the hint is an addition, not a replacement.
	for _, want := range []string{"alpaca.snuglab.local:8188", "alpaca.snuglab.locl:8188"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the message must still name %q:\n%s", want, msg)
		}
	}
	// The divergent tails are what make the difference visible.
	if !strings.Contains(msg, `"al:8188"`) || !strings.Contains(msg, `"l:8188"`) {
		t.Errorf("the message must show where the two diverge:\n%s", msg)
	}
	if !strings.Contains(msg, "typo") {
		t.Errorf("the message must name the likely cause:\n%s", msg)
	}
}

// Two genuinely different machines are not a typo, and saying "identical up to
// character 1" would be noise on top of a message that already explains itself.
func TestPlainlyDifferentHostsGetNoTypoHint(t *testing.T) {
	err := sameImageHost("http://comfy-a.example.com:8188/prompt", "http://render-farm.internal:9000/upload/image")
	if err == nil {
		t.Fatal("mismatched hosts must be an error")
	}
	if strings.Contains(err.Error(), "typo") {
		t.Errorf("unrelated hosts should not be called a typo:\n%s", err.Error())
	}
}

func TestNearIdenticalHosts(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
		why  string
	}{
		{"alpaca.snuglab.local:8188", "alpaca.snuglab.locl:8188", true, "one deletion — the reported case"},
		{"box:8188", "box:8189", true, "one substitution, a mistyped port"},
		{"box.local:8188", "box.locall:8188", true, "one insertion"},
		{"box.local:8188", "box.lcoal:8188", true, "transposition is two edits"},
		{"comfy-a.example.com", "comfy-b.example.com", true, "one character — genuinely ambiguous, and worth flagging"},
		{"comfy.example.com:8188", "render-farm.internal:9000", false, "different machines"},
		{"box:8188", "", false, "nothing to compare against"},
		{"", "box:8188", false, "nothing to compare against"},
		{"a.local:8188", "a.local:8188", true, "identical, though the caller never asks"},
	} {
		if got := nearIdenticalHosts([]rune(tc.a), []rune(tc.b)); got != tc.want {
			t.Errorf("nearIdenticalHosts(%q, %q) = %v, want %v (%s)", tc.a, tc.b, got, tc.want, tc.why)
		}
	}
}

// A long host that differs only near the end must not be dismissed as
// "different servers" just because the strings are long.
func TestALongHostWithALateTypoIsStillCaught(t *testing.T) {
	a := "comfyui-render-01.lab.internal.example.com:8188"
	b := "comfyui-render-01.lab.internal.exampel.com:8188"
	hint := hostTypoHint(a, b)
	if hint == "" {
		t.Fatalf("a late typo in a long host must still be pointed at:\n%s\n%s", a, b)
	}
	if !strings.Contains(hint, "typo") {
		t.Errorf("hint should name the cause: %s", hint)
	}
}
