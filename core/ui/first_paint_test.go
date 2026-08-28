package ui

import (
	"bytes"
	"strings"
	"testing"
)

// TestFirstPaintIsAlreadyThemed — the runtime stylesheet is an external link
// served no-cache, so every navigation asks for it before the new document can
// paint. Until it answers, even with a 304, the browser has only its own
// default canvas: white. Moving between two hub apps that look identical either
// side of it produced a full white frame in between.
func TestFirstPaintIsAlreadyThemed(t *testing.T) {
	render := func(theme string) string {
		var b bytes.Buffer
		if err := RenderPageJSON(&b, []byte(`{}`), theme, "", "T"); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}

	dark := render("indigo")
	if !strings.Contains(dark, `<meta name="color-scheme" content="dark">`) {
		t.Error("no color-scheme on a dark theme; the browser paints its default white canvas and the scrollbars come out white too")
	}
	if !strings.Contains(dark, "html,body{background:#0f1117") {
		t.Error("the page's own background is not pinned before the stylesheet loads")
	}

	// ORDER is the whole point. After the stylesheet link these would still be
	// correct and still arrive too late to prevent anything.
	sheet := strings.Index(dark, `<link rel="stylesheet"`)
	scheme := strings.Index(dark, `name="color-scheme"`)
	inline := strings.Index(dark, "html,body{background:")
	if scheme < 0 || inline < 0 || sheet < 0 {
		t.Fatal("head is missing pieces this test depends on")
	}
	if scheme > sheet || inline > sheet {
		t.Error("the first-paint head comes AFTER the stylesheet link, which is the one position where it changes nothing")
	}

	// A light theme has to say so: color-scheme drives scrollbar and default
	// form-control rendering, so announcing the wrong one is not cosmetic.
	light := render("light")
	if !strings.Contains(light, `content="light"`) {
		t.Error("the light theme announces itself dark")
	}
	if !strings.Contains(light, "html,body{background:#f6f7f9") {
		t.Error("the light theme's background is not pinned")
	}

	// An unregistered theme leaves the document exactly as it was.
	if got := render("no-such-theme"); strings.Contains(got, "color-scheme") {
		t.Error("an unknown theme invented a color-scheme; it should leave the old behaviour alone")
	}
}

// TestLightColorInference — inferred from the background rather than declared,
// so a theme registered from outside this package cannot forget to say.
func TestLightColorInference(t *testing.T) {
	for _, c := range []struct {
		hex   string
		light bool
	}{
		{"#0f1117", false}, // indigo
		{"#f6f7f9", true},  // light
		{"#fff", true},     // three-digit shorthand
		{"#000", false},
		{"", false}, // unparseable falls to dark, which is the default
		{"not-a-color", false},
	} {
		if got := isLightColor(c.hex); got != c.light {
			t.Errorf("isLightColor(%q) = %v, want %v", c.hex, got, c.light)
		}
	}
}
