package imagefetch

import "testing"

// An image's file name counts as text about it, and a name that says nothing
// changes nothing. For a named person it counts when it carries their whole
// name, as a page naming them would.
func TestTheImageFileNameHelpsFindTheRightImage(t *testing.T) {
	cases := map[string]string{
		"https://upload.wikimedia.org/x/Golden_Gate_Bridge_at_sunset.jpg": "golden gate bridge at sunset",
		"https://cdn.example.com/a/b/acme-logo-2024.png?w=600":            "acme logo 2024",
		"https://example.com/photos/Mount%20Rainier%2C%20WA.webp":         "mount rainier wa",
		"https://cdn.example.com/8f3a2c91e0.jpg":                          "8f3a2c91e0",
		"":                                                                "",
	}
	for in, want := range cases {
		if got := imageFileWords(in); got != want {
			t.Errorf("imageFileWords(%q) = %q, want %q", in, got, want)
		}
	}

	bridge := []string{"https://upload.wikimedia.org/x/Golden_Gate_Bridge_at_sunset.jpg"}
	if !candidateTextMatch("golden gate bridge sunset", false, "", bridge) {
		t.Error("a file name naming the subject did not count")
	}
	if candidateTextMatch("golden gate bridge sunset", false, "", []string{"https://cdn.example.com/IMG_1234.jpg"}) {
		t.Error("a meaningless file name counted as a match")
	}
	if !candidateTextMatch("golden gate bridge sunset", true, "", nil) {
		t.Error("the page's own mention stopped counting")
	}
	if !candidateTextMatch("Jane Doe on stage", false, "", []string{"https://x.example/jane-doe-on-stage.jpg"}) {
		t.Error("a file name carrying the person's whole name did not count")
	}
	if candidateTextMatch("Jane Doe on stage", false, "", []string{"https://x.example/jane-on-stage.jpg"}) {
		t.Error("a file name with only part of the person's name counted")
	}
	if !candidateTextMatch("Jane Doe on stage", false, "Jane Doe on stage in Denver", nil) {
		t.Error("a title naming the person stopped counting")
	}
}

// A search for a person often has the right picture below the first few,
// with a file name that says who it is: those go first, in the search's own
// order, so they fall inside the candidates that get screened.
func TestFileNamesNamingTheSubjectAreTriedFirst(t *testing.T) {
	in := []SerperImageResult{
		{ImageURL: "https://a.example/IMG_0001.jpg"},
		{ImageURL: "https://b.example/crowd-at-concert.jpg"},
		{ImageURL: "https://c.example/jane-doe-portrait.jpg"},
		{ImageURL: "https://d.example/8f3a2c.jpg"},
		{ImageURL: "https://e.example/Jane_Doe_2019.png"},
	}
	got := fileNameMatchesFirst(in, "Jane Doe")
	order := []string{}
	for _, r := range got {
		order = append(order, r.ImageURL)
	}
	if order[0] != "https://c.example/jane-doe-portrait.jpg" || order[1] != "https://e.example/Jane_Doe_2019.png" || order[2] != "https://a.example/IMG_0001.jpg" || len(order) != 5 {
		t.Errorf("order = %v", order)
	}
}
