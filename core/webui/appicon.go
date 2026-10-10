package webui

// Add to Home Screen: the tags, the manifest and the PNG icons that let a
// phone keep oddjob on its home screen and open it without the browser's
// chrome.
//
// The icons are drawn from IconSVG itself (its rectangles are read out of the
// markup), so the home-screen icon cannot drift from the favicon. They are
// PNG because iOS will not use an SVG for a home-screen icon, and plain Go
// draws them: the mark is three rounded squares on a rounded square, which
// needs no SVG renderer.

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"math"
	"regexp"
	"strconv"
	"sync"
)

// AppHeadTags go in every page's <head>: the manifest, the home-screen icon,
// and the flags that open the saved page as an app. theme-color is added by
// callers that know the page's theme.
const AppHeadTags = `<link rel="manifest" href="/manifest.webmanifest">
<link rel="apple-touch-icon" href="/apple-touch-icon.png">
<meta name="apple-mobile-web-app-capable" content="yes">
<meta name="mobile-web-app-capable" content="yes">
<meta name="apple-mobile-web-app-title" content="oddjob">
`

// AppIconPaths are the public paths the icons are served at, for the router.
var AppIconPaths = []string{"/manifest.webmanifest", "/apple-touch-icon.png", "/_ui/icon-192.png", "/_ui/icon-512.png", "/_ui/icon-maskable-512.png"}

// ManifestJSON is the web app manifest; themeColor is the active theme's
// background, so the phone's status bar and splash match the pages.
func ManifestJSON(themeColor string) []byte {
	if themeColor == "" {
		themeColor = iconBackground()
	}
	m := map[string]any{
		"name":             "oddjob",
		"short_name":       "oddjob",
		"start_url":        "/",
		"scope":            "/",
		"display":          "standalone",
		"background_color": themeColor,
		"theme_color":      themeColor,
		"icons": []map[string]string{
			{"src": "/_ui/icon-192.png", "sizes": "192x192", "type": "image/png", "purpose": "any"},
			{"src": "/_ui/icon-512.png", "sizes": "512x512", "type": "image/png", "purpose": "any"},
			{"src": "/_ui/icon-maskable-512.png", "sizes": "512x512", "type": "image/png", "purpose": "maskable"},
		},
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	return b
}

// iconRect is one <rect> of IconSVG, in its 64-unit viewBox.
type iconRect struct {
	x, y, w, h, rx float64
	fill           color.NRGBA
}

var (
	rectRe  = regexp.MustCompile(`<rect\b[^>]*>`)
	attrRe  = regexp.MustCompile(`(\w+)="([^"]*)"`)
	iconBox = 64.0
)

// iconRects reads the rectangles out of IconSVG, in drawing order.
func iconRects() []iconRect {
	var out []iconRect
	for _, tag := range rectRe.FindAllString(IconSVG, -1) {
		r := iconRect{fill: color.NRGBA{A: 255}}
		for _, m := range attrRe.FindAllStringSubmatch(tag, -1) {
			v, _ := strconv.ParseFloat(m[2], 64)
			switch m[1] {
			case "x":
				r.x = v
			case "y":
				r.y = v
			case "width":
				r.w = v
			case "height":
				r.h = v
			case "rx":
				r.rx = v
			case "fill":
				r.fill = parseHex(m[2])
			}
		}
		out = append(out, r)
	}
	return out
}

func parseHex(s string) color.NRGBA {
	if len(s) != 7 || s[0] != '#' {
		return color.NRGBA{A: 255}
	}
	n, err := strconv.ParseUint(s[1:], 16, 32)
	if err != nil {
		return color.NRGBA{A: 255}
	}
	return color.NRGBA{R: uint8(n >> 16), G: uint8(n >> 8), B: uint8(n), A: 255}
}

// iconBackground is the mark's backing square colour, as #rrggbb.
func iconBackground() string {
	rs := iconRects()
	if len(rs) == 0 {
		return "#0f1117"
	}
	c := rs[0].fill
	return "#" + hex2(c.R) + hex2(c.G) + hex2(c.B)
}

func hex2(b uint8) string {
	const digits = "0123456789abcdef"
	return string([]byte{digits[b>>4], digits[b&15]})
}

// inRoundRect reports whether (px, py) falls inside a rounded rectangle.
func inRoundRect(px, py, x, y, w, h, rx float64) bool {
	if px < x || py < y || px > x+w || py > y+h {
		return false
	}
	rx = math.Min(rx, math.Min(w, h)/2)
	cx := math.Min(math.Max(px, x+rx), x+w-rx)
	cy := math.Min(math.Max(py, y+rx), y+h-rx)
	dx, dy := px-cx, py-cy
	return dx*dx+dy*dy <= rx*rx
}

// drawIcon renders the mark at size pixels. fullBleed fills the whole square
// with the backing colour (no rounded corners, no transparency) and draws
// the squares within the middle `inner` of it: iOS and Android cut their own
// shape out of a home-screen icon, and a maskable icon's safe zone is the
// centre circle of 80%.
func drawIcon(size int, fullBleed bool, inner float64) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	rs := iconRects()
	if len(rs) == 0 {
		return img
	}
	scale := float64(size) / iconBox
	shapes := rs
	if fullBleed {
		bg := rs[0].fill
		for i := 0; i < len(img.Pix); i += 4 {
			img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = bg.R, bg.G, bg.B, 255
		}
		shapes = rs[1:]
		// The squares span a box inside the viewBox; centre that box and
		// scale it to fill `inner` of the icon.
		minX, minY, maxX, maxY := iconBox, iconBox, 0.0, 0.0
		for _, r := range shapes {
			minX, minY = math.Min(minX, r.x), math.Min(minY, r.y)
			maxX, maxY = math.Max(maxX, r.x+r.w), math.Max(maxY, r.y+r.h)
		}
		span := math.Max(maxX-minX, maxY-minY)
		k := inner * iconBox / span
		ox, oy := (iconBox-(maxX-minX)*k)/2, (iconBox-(maxY-minY)*k)/2
		moved := make([]iconRect, len(shapes))
		for i, r := range shapes {
			moved[i] = iconRect{x: ox + (r.x-minX)*k, y: oy + (r.y-minY)*k, w: r.w * k, h: r.h * k, rx: r.rx * k, fill: r.fill}
		}
		shapes = moved
	}
	const ss = 4 // 4x4 samples a pixel, for smooth edges
	for py := 0; py < size; py++ {
		for px := 0; px < size; px++ {
			for _, r := range shapes {
				hits := 0
				for sy := 0; sy < ss; sy++ {
					for sx := 0; sx < ss; sx++ {
						ux := (float64(px) + (float64(sx)+0.5)/ss) / scale
						uy := (float64(py) + (float64(sy)+0.5)/ss) / scale
						if inRoundRect(ux, uy, r.x, r.y, r.w, r.h, r.rx) {
							hits++
						}
					}
				}
				if hits == 0 {
					continue
				}
				blend(img, px, py, r.fill, float64(hits)/(ss*ss))
			}
		}
	}
	return img
}

// blend paints c over the pixel at coverage a (source-over).
func blend(img *image.NRGBA, x, y int, c color.NRGBA, a float64) {
	i := img.PixOffset(x, y)
	p := img.Pix[i : i+4]
	da := float64(p[3]) / 255
	oa := a + da*(1-a)
	if oa == 0 {
		return
	}
	mix := func(s, d uint8) uint8 {
		return uint8(math.Round((float64(s)*a + float64(d)*da*(1-a)) / oa))
	}
	p[0], p[1], p[2] = mix(c.R, p[0]), mix(c.G, p[1]), mix(c.B, p[2])
	p[3] = uint8(math.Round(oa * 255))
}

var (
	iconMu    sync.Mutex
	iconCache = map[string][]byte{}
)

// AppIconPNG returns the icon served at one of AppIconPaths (not the
// manifest), drawn once and kept.
func AppIconPNG(path string) ([]byte, bool) {
	var size int
	var fullBleed bool
	inner := 0.0
	switch path {
	case "/apple-touch-icon.png":
		size, fullBleed, inner = 180, true, 0.72
	case "/_ui/icon-192.png":
		size = 192
	case "/_ui/icon-512.png":
		size = 512
	case "/_ui/icon-maskable-512.png":
		size, fullBleed, inner = 512, true, 0.6
	default:
		return nil, false
	}
	iconMu.Lock()
	defer iconMu.Unlock()
	if b, ok := iconCache[path]; ok {
		return b, true
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, drawIcon(size, fullBleed, inner)); err != nil {
		return nil, false
	}
	iconCache[path] = buf.Bytes()
	return iconCache[path], true
}
