package scribe

// Pictures in a guide are stored once beside it and linked from its markdown,
// so History copies a link and not the picture. What leaves gohort (exports,
// publish, bundles) carries them embedded, and what arrives embedded (a bundle,
// a re-imported export) lands back in the store.

import (
	"encoding/base64"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A 1x1 PNG: real bytes, so content sniffing agrees it is one.
var tinyPNG, _ = base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==")

func TestImagesTravelEmbeddedAndLandStored(t *testing.T) {
	udb := UserDB(&DBase{Store: kvlite.MemStore()}, "u")
	T := &Scribe{}
	g := Guide{ID: "g1", Sections: []Section{{ID: "s", Title: "Steps", Order: 1,
		Markdown: "Click here:\n\n![Login](" + T.guideImagePath("g1", "i1") + ")\n\n![Gone](" + T.guideImagePath("g1", "nope") + ")"}}}
	udb.Set(guideImagesTable, guideImageKey("g1", "i1"), guideImage{Mime: "image/png", Data: tinyPNG})

	// Out: a stored picture is embedded; a missing one is left as it was.
	out := inlineGuideImages(g, udb)
	md := out.Sections[0].Markdown
	if !strings.Contains(md, "![Login](data:image/png;base64,") {
		t.Fatalf("stored picture not embedded:\n%s", md)
	}
	if !strings.Contains(md, "i=nope)") {
		t.Errorf("a reference to nothing should stay as it was:\n%s", md)
	}
	if g.Sections[0].Markdown == md {
		t.Error("inlineGuideImages must return a copy, not change the guide")
	}

	// Back in, under a new guide: the embedded picture becomes a stored one.
	in := Guide{ID: "g2", Sections: out.Sections}
	T.storeEmbeddedImages(&in, udb)
	md = in.Sections[0].Markdown
	if strings.Contains(md, "data:image") || !strings.Contains(md, "/img?g=g2&i=") {
		t.Fatalf("embedded picture not stored:\n%s", md)
	}
	ref := guideImageRefRE.FindStringSubmatch(md)
	var img guideImage
	if ref == nil || !udb.Get(guideImagesTable, guideImageKey(ref[1], ref[2]), &img) || img.Mime != "image/png" {
		t.Errorf("stored picture not found under %v", ref)
	}

	// Something claiming to be a PNG that is not one stays put.
	fake := Guide{ID: "g3", Sections: []Section{{Markdown: "![x](data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("<svg onload=alert(1)>")) + ")"}}}
	T.storeEmbeddedImages(&fake, udb)
	if !strings.Contains(fake.Sections[0].Markdown, "data:image/png") {
		t.Error("bytes that are not a raster must not be stored as one")
	}

	// Deleting a guide takes its pictures and only its pictures.
	deleteGuideImages(udb, "g1")
	if udb.Get(guideImagesTable, guideImageKey("g1", "i1"), &img) {
		t.Error("the deleted guide's picture survived")
	}
	if !udb.Get(guideImagesTable, guideImageKey(ref[1], ref[2]), &img) {
		t.Error("another guide's picture went with it")
	}
}

func TestImageAltFromFileName(t *testing.T) {
	for in, want := range map[string]string{
		"image.png": "Screenshot", "": "Screenshot",
		"login_page-v2.png": "login page v2", "a[b].png": "ab",
	} {
		if got := imageAltFrom(in); got != want {
			t.Errorf("imageAltFrom(%q) = %q, want %q", in, got, want)
		}
	}
}
