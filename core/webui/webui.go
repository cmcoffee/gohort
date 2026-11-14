// Package webui provides shared web UI scaffolding for gohort apps.
//
// Goal: eliminate duplicated CSS, JS, and HTML chrome across web
// frontends: the shared theme CSS, the favicon and app icons, and the
// manifest. Pages are assembled by core/ui; the old RenderPage assembler
// and its utility script went with their last caller.
//
// All static assets are embedded via go:embed, so the binary remains
// single-file. Apps can also serve raw assets via /webui/<file> if they
// prefer browser-cached external resources later.
package webui

import (
	"embed"
	"encoding/base64"
	"strings"
	"sync"
)

//go:embed static
var staticFS embed.FS

// asset returns the contents of an embedded static file. Panics on
// missing files because asset names are compile-time constants.
func asset(name string) string {
	b, err := staticFS.ReadFile("static/" + name)
	if err != nil {
		panic("webui: missing asset " + name + ": " + err.Error())
	}
	return string(b)
}

// BaseCSS returns the shared dark theme CSS used by all apps. Apps may
// append their own styles. Includes self-hosted font @font-face declarations so app headers can
// reference 'Space Grotesk' without loading anything external.
func BaseCSS() string { return fontFaceCSS() + asset("base.css") }

var (
	fontFaceOnce sync.Once
	fontFaceStr  string
)

// FontFaceCSS returns the @font-face declaration for Orbitron Bold
// (700), with the font binary inlined as a base64 data URL. No
// external HTTP request.
// Size: ~9KB of base64 text. Loaded once and memoized.
//
// Exposed for apps that render their own HTML templates: they can inject
// this into their own <style> block or <head> to pick up the shared header
// font.
func FontFaceCSS() string { return fontFaceCSS() }

func fontFaceCSS() string {
	fontFaceOnce.Do(func() {
		raw, err := staticFS.ReadFile("static/fonts/orbitron-700-latin.woff2")
		if err != nil {
			// Missing font asset is non-fatal — pages render with
			// system font fallback. Log via panic-on-asset is too
			// strong for a stylistic enhancement.
			fontFaceStr = ""
			return
		}
		encoded := base64.StdEncoding.EncodeToString(raw)
		fontFaceStr = "@font-face{" +
			"font-family:'Orbitron';" +
			"font-style:normal;" +
			"font-weight:700;" +
			"font-display:swap;" +
			"src:url(data:font/woff2;base64," + encoded + ") format('woff2');" +
			"unicode-range:U+0000-00FF,U+0131,U+0152-0153,U+02BB-02BC,U+02C6,U+02DA,U+02DC,U+0304,U+0308,U+0329,U+2000-206F,U+20AC,U+2122,U+2191,U+2193,U+2212,U+2215,U+FEFF,U+FFFD;" +
			"}\n"
	})
	return fontFaceStr
}


// IconSVG is the raw inline SVG markup for gohort's mark, and the single
// source of truth for it. Used wherever a surface wants the icon as inline
// HTML, so the favicon and the on-page branding cannot disagree. Apps that
// want a different icon override via WebIconer.WebIcon() in core; an empty
// return falls back to this default.
//
// The mark is "Vanguard": one bright lead unit with the cohort behind it.
// A cohort is what the name means, and a fleet rather than one assistant is
// the thing that distinguishes the platform, so the mark argues that and not
// "an AI product" (the robot head this replaces said the latter, in a palette
// the dashboard had already left behind).
//
// Design notes: three solid squares on a dark rounded square, indigo #6366f1
// leading and #4338ca behind, on the platform's own slate #0f1117. Deliberately
// free of strokes and fine detail — a favicon is judged at 16×16, where that
// budget buys about three shapes and the gaps here still hold four device
// pixels of clear air. Anything finer turns into a coloured smudge.
//
// No background knockouts either: every shape is drawn, nothing is erased by
// painting slate over it. That keeps the artwork correct with no chip behind
// it, which is what a macOS menu-bar template image needs — so one drawing can
// serve the favicon, the app icon, and the tray.
const IconSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64">` +
	`<rect width="64" height="64" rx="12" fill="#0f1117"/>` +
	`<rect x="23" y="8" width="18" height="18" rx="5" fill="#6366f1"/>` +
	`<rect x="6" y="34" width="18" height="18" rx="5" fill="#4338ca"/>` +
	`<rect x="40" y="34" width="18" height="18" rx="5" fill="#4338ca"/>` +
	`</svg>`

// FaviconSVG is IconSVG encoded for a data: URI, for the <link rel="icon">
// href. DERIVED, never hand-written: this used to be a second constant kept
// "in lockstep" with the markup by a comment asking whoever edited one to
// remember the other, which is a drift waiting to happen and buys nothing —
// the transform is mechanical.
var FaviconSVG = svgDataURI(IconSVG)

// svgDataURI renders raw SVG markup for use inside a data:image/svg+xml URI
// that itself sits in a double-quoted HTML attribute.
//
// Attribute quotes are swapped to single so the markup can't terminate the
// href, and the four characters a browser will not accept raw in that position
// are percent-encoded. Spaces are left alone: they are legal inside a quoted
// attribute and encoding them only makes the URI harder to read in a view-source.
//
// strings.Replacer matches against the INPUT in one pass and never rescans what
// it emitted, so encoding "%" alongside the rest cannot double-encode the
// escapes this produces.
func svgDataURI(svg string) string {
	return strings.NewReplacer(
		"%", "%25",
		`"`, "'",
		"<", "%3C",
		">", "%3E",
		"#", "%23",
	).Replace(svg)
}


