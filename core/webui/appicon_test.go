package webui

import (
	"bytes"
	"encoding/json"
	"image/png"
	"testing"
)

// The home-screen icons are real PNGs at their sizes, drawn from the mark: an
// "any" icon keeps the mark's rounded corners (transparent outside them), a
// full-bleed one (iOS, maskable) is solid to the edge in the mark's backing
// colour with the lead square's indigo inside.
func TestTheHomeScreenIconsAreDrawnFromTheMark(t *testing.T) {
	for path, want := range map[string]int{"/apple-touch-icon.png": 180, "/_ui/icon-192.png": 192, "/_ui/icon-512.png": 512, "/_ui/icon-maskable-512.png": 512} {
		b, ok := AppIconPNG(path)
		if !ok {
			t.Fatalf("%s is not served", path)
		}
		img, err := png.Decode(bytes.NewReader(b))
		if err != nil || img.Bounds().Dx() != want || img.Bounds().Dy() != want {
			t.Fatalf("%s: a %dpx PNG: %v %v", path, want, img.Bounds(), err)
		}
		_, _, _, cornerA := img.At(0, 0).RGBA()
		full := path == "/apple-touch-icon.png" || path == "/_ui/icon-maskable-512.png"
		if full && cornerA != 0xffff {
			t.Errorf("%s is solid to the corner (the phone cuts its own shape)", path)
		}
		if !full && cornerA != 0 {
			t.Errorf("%s keeps the mark's rounded corner", path)
		}
		r, g, bl, _ := img.At(want/2, want/2-want/4).RGBA()
		if !(bl>>8 > 200 && r>>8 < 130 && g>>8 < 130) {
			t.Errorf("%s: the lead square's indigo sits top-centre, got %d,%d,%d", path, r>>8, g>>8, bl>>8)
		}
	}
	if _, ok := AppIconPNG("/nope.png"); ok {
		t.Error("only the listed icons are served")
	}
}

// The manifest opens the app standalone at the dashboard, in the page's
// colour, and lists every icon served.
func TestTheManifestNamesTheAppAndItsIcons(t *testing.T) {
	var m struct {
		Name, Display, StartURL, ThemeColor string
		Icons                               []struct{ Src, Purpose string }
	}
	raw := ManifestJSON("#101010")
	if err := json.Unmarshal(bytes.ReplaceAll(bytes.ReplaceAll(raw, []byte(`"start_url"`), []byte(`"StartURL"`)), []byte(`"theme_color"`), []byte(`"ThemeColor"`)), &m); err != nil {
		t.Fatal(err)
	}
	if m.Name != "gohort" || m.Display != "standalone" || m.StartURL != "/" || m.ThemeColor != "#101010" || len(m.Icons) != 3 {
		t.Errorf("manifest: %+v", m)
	}
	served := map[string]bool{}
	for _, p := range AppIconPaths {
		served[p] = true
	}
	for _, ic := range m.Icons {
		if !served[ic.Src] {
			t.Errorf("%s is listed but not served", ic.Src)
		}
	}
	var def struct{ ThemeColor string `json:"theme_color"` }
	json.Unmarshal(ManifestJSON(""), &def)
	if def.ThemeColor != "#0f1117" {
		t.Errorf("with no theme, the mark's own backing colour: %q", def.ThemeColor)
	}
}
