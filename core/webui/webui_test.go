package webui

import (
	"strings"
	"testing"
)

// The favicon used to be a second hand-written constant kept in lockstep with
// the icon markup by a comment. These are the checks that replace the comment.

// Nothing that would terminate the href or truncate the URI may survive into
// the encoded form. A raw '#' is the interesting one: it reads as a fragment
// delimiter, so an un-encoded fill colour silently cuts the icon off at the
// first swatch and the tab renders a blank square.
func TestFaviconSVGCarriesNothingRawIntoTheAttribute(t *testing.T) {
	for _, bad := range []string{"<", ">", "#", `"`} {
		if strings.Contains(FaviconSVG, bad) {
			t.Errorf("encoded favicon still contains a raw %q", bad)
		}
	}
	if !strings.HasPrefix(FaviconSVG, "%3Csvg ") {
		t.Errorf("encoded favicon should open with an encoded <svg, got %.20q", FaviconSVG)
	}
}

// The derivation must be lossless: whatever ships in the href has to be the
// same drawing as the inline markup, which is the property the old pair of
// constants asked a human to maintain.
func TestFaviconSVGDecodesBackToTheIconMarkup(t *testing.T) {
	back := strings.NewReplacer(
		"%3C", "<",
		"%3E", ">",
		"%23", "#",
		"%25", "%",
	).Replace(FaviconSVG)
	// Attribute quotes are the one deliberate change, so compare against the
	// markup with the same swap applied.
	want := strings.ReplaceAll(IconSVG, `"`, "'")
	if back != want {
		t.Errorf("round trip lost the drawing:\n got %s\nwant %s", back, want)
	}
}

// Encoding "%" in the same pass as the rest must not re-encode the escapes the
// pass itself produces. strings.Replacer never rescans its output, and this
// pins that assumption rather than trusting it.
func TestSVGDataURIDoesNotDoubleEncode(t *testing.T) {
	if got, want := svgDataURI(`<a v="50%"/>`), "%3Ca v='50%25'/%3E"; got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

// The mark is judged at 16x16, where one viewBox unit is a quarter of a device
// pixel. Solid shapes only is what keeps it legible there, so a stroke turning
// up in the icon is a design regression rather than a style nit: a 4-unit
// stroke is one device pixel and the downsample eats it.
func TestIconIsStrokeFree(t *testing.T) {
	if strings.Contains(IconSVG, "stroke") {
		t.Error("the mark uses a stroke; thin geometry does not survive 16x16")
	}
}

// Nothing in the mark may be erased by painting the slate back over it. A
// knockout looks identical on the dashboard and falls apart the moment the
// artwork is used without a chip behind it, which is exactly what a macOS
// menu-bar template image is. Every shape after the chip must carry its own
// colour, so the background fill may appear exactly once.
func TestIconHasNoBackgroundKnockouts(t *testing.T) {
	if n := strings.Count(IconSVG, "#0f1117"); n != 1 {
		t.Errorf("slate appears %d times; more than once means a shape is being erased rather than drawn", n)
	}
}
