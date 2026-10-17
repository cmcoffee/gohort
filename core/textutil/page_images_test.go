package textutil

import (
	"net/url"
	"strings"
	"testing"
)

// A page's content images are listed with their alt text and absolute
// address; furniture, icons, data URIs and SVGs are not.
func TestPageImagesListsContentNotFurniture(t *testing.T) {
	base, _ := url.Parse("https://example.org/news/story.html")
	page := `<html><body>
<img src="/img/logo.png" alt="Example News">
<img src="data:image/gif;base64,R0lGOD" alt="spacer">
<img src="icons/share.svg" alt="share">
<img src="/t.gif" width="1" height="1">
<img src="charts/q3-revenue.png?utm_source=feed&utm_medium=x" alt="  Q3 revenue
 by region  ">
<img data-src="https://cdn.example.org/board.jpg" src="data:image/gif;base64,AAAA" alt="whiteboard photo">
<img srcset="/hero-800.jpg 800w, /hero-1600.jpg 1600w" alt="">
<img src="charts/q3-revenue.png" alt="duplicate">
<img src="/photo.jpg?w=800&utm_campaign=y" alt="photo">
</body></html>`
	got := PageImages(page, base, 0)
	want := []PageImage{
		{URL: "https://example.org/news/charts/q3-revenue.png", Alt: "Q3 revenue by region"},
		{URL: "https://cdn.example.org/board.jpg", Alt: "whiteboard photo"},
		{URL: "https://example.org/hero-800.jpg", Alt: ""},
		{URL: "https://example.org/photo.jpg?w=800", Alt: "photo"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d images: %+v", len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("image %d: got %+v, want %+v", i, got[i], want[i])
		}
	}
	if n := len(PageImages(page, base, 2)); n != 2 {
		t.Errorf("the cap was not kept: %d", n)
	}
	note := PageImagesNote(got, `image(action="fetch", url=...)`)
	if !strings.Contains(note, "image(action=\"fetch\"") || !strings.Contains(note, "- (no description): https://example.org/hero-800.jpg") {
		t.Errorf("the note should say how to look and list each:\n%s", note)
	}
	if PageImagesNote(nil, "x") != "" {
		t.Error("no images is no note")
	}
	if PageImagesNote(got, "") != "" {
		t.Error("a list the reader cannot open is no note")
	}
}

// A wall is short and carries a marker; a short answer is not a wall.
func TestIsWallPage(t *testing.T) {
	if !IsWallPage("Just a moment... Checking your browser before accessing example.org.") {
		t.Error("a bot check was not a wall")
	}
	if IsWallPage(`{"ok":true,"count":3}`) {
		t.Error("a short API answer was taken for a wall")
	}
	if IsWallPage(strings.Repeat("A long article that mentions a cookie once. ", 60)) {
		t.Error("a long page with a footer mention was taken for a wall")
	}
}

// The way to look is named from the tools the reader has, or not at all.
func TestImageLookHintNamesOnlyAToolTheReaderHas(t *testing.T) {
	has := func(tools ...string) func(string) bool {
		return func(n string) bool {
			for _, t := range tools {
				if t == n {
					return true
				}
			}
			return false
		}
	}
	if got := ImageLookHint(has("image", "fetch_image")); !strings.HasPrefix(got, "image(") {
		t.Errorf("with the image tool: %q", got)
	}
	if got := ImageLookHint(has("fetch_image")); !strings.HasPrefix(got, "fetch_image(") {
		t.Errorf("with fetch_image only: %q", got)
	}
	if got := ImageLookHint(has("web_search")); got != "" {
		t.Errorf("with neither, nothing is named: %q", got)
	}
	if got := ImageLookHint(nil); got != "" {
		t.Errorf("not knowing is naming nothing: %q", got)
	}
}
