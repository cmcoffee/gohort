package orchestrate

// Articles to fetch: the owner's list of documents a collection must hold.
// What these hold is that each is fetched once, the list says which are in,
// and a long auto-fill run does not undo what the owner changed meanwhile.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func TestArticlesAreTidiedAndCapped(t *testing.T) {
	got, err := normalizeArticles([]string{
		"  IRS   Publication 17 ", "", "irs publication 17",
		"https://example.com/a/", "https://Example.com/a",
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, "|") != "IRS Publication 17|https://example.com/a/" {
		t.Errorf("repeats, by wording or by address, should go: %q", got)
	}
	if _, err := normalizeArticles([]string{strings.Repeat("x", maxArticleChars+1)}); err == nil {
		t.Error("an entry past the length cap was taken")
	}
	var many []string
	for i := 0; i <= maxCollectionArticles; i++ {
		many = append(many, "article "+string(rune('a'+i%26))+strings.Repeat("!", i))
	}
	if _, err := normalizeArticles(many); err == nil || !strings.Contains(err.Error(), "at most") {
		t.Errorf("a list past the cap was taken, or cut without saying: %v", err)
	}
}

func TestALinkIsFetchedAsIsAndADescriptionIsSearched(t *testing.T) {
	for in, want := range map[string]bool{
		"https://example.com/doc.pdf":   true,
		"HTTP://example.com":            true,
		"https://example.com is a site": false,
		"IRS Publication 17":            false,
		"example.com/doc":               false,
	} {
		if got := isArticleLink(in); got != want {
			t.Errorf("isArticleLink(%q) = %v, want %v", in, got, want)
		}
	}
}

// A fetched article is not fetched again, and a link already in the
// collection by another road (an upload of the same page, an earlier
// auto-fill) counts as in.
func TestOnlyArticlesNotInYetArePending(t *testing.T) {
	c := Collection{
		Articles:        []string{"Fetched one", "Waiting one", "https://example.com/in", "https://example.com/out"},
		ArticlesFetched: map[string]string{"Fetched one": "https://found.example/1"},
		IngestedURLs:    []string{"https://example.com/in/"},
	}
	if got := strings.Join(pendingArticles(c), "|"); got != "Waiting one|https://example.com/out" {
		t.Errorf("pending = %q", got)
	}
	rows := articleRows(c)
	from := map[string]string{}
	for _, r := range rows {
		from[r["article"]] = r["fetched_from"]
	}
	if from["Fetched one"] != "https://found.example/1" || from["https://example.com/in"] == "" || from["Waiting one"] != "" {
		t.Errorf("the page would show the wrong state: %v", from)
	}
}

func TestSearchResultsKeepTheirTitles(t *testing.T) {
	raw := "1. The Standard\n   https://std.example/spec.pdf\n   A snippet.\n\n2. No address here\n   just text\n\n3. Second\n   http://two.example\n"
	got := parseWebSearchResults(raw)
	if len(got) != 2 || got[0].Title != "The Standard" || got[0].URL != "https://std.example/spec.pdf" || got[1].URL != "http://two.example" {
		t.Errorf("parsed %+v", got)
	}
}

// The run's marks land on the record as it is NOW. A run takes minutes, and
// saving the copy it started with would undo the owner's edits made meanwhile.
func TestARunDoesNotUndoEditsMadeWhileItRan(t *testing.T) {
	store := stewardStores(t)
	udb := store("alice")
	T := &OrchestrateApp{AppCore: AppCore{DB: CollectionsDB()}}
	start := Collection{ID: "col-a", Name: "Tax", Owner: "alice", Articles: []string{"Pub 17"}}
	saveCollection(udb, start)

	// Meanwhile the owner adds an article and rewrites the description.
	edited := start
	edited.Articles = []string{"Pub 17", "Pub 501"}
	edited.Description = "edited"
	saveCollection(udb, edited)

	run := start
	T.recordIngest(udb, &run, "https://irs.example/p17.pdf", "Pub 17")

	got, _ := loadCollection(udb, "alice", "col-a")
	if got.Description != "edited" || len(got.Articles) != 2 {
		t.Errorf("the run undid the owner's edits: %+v", got)
	}
	if got.ArticlesFetched["Pub 17"] != "https://irs.example/p17.pdf" || !collectionHasURL(got, "https://irs.example/p17.pdf") {
		t.Errorf("the run's own marks are missing: %+v", got)
	}
	if run.ArticlesFetched["Pub 17"] == "" {
		t.Error("the run did not see its own mark")
	}
	// An article taken off the list meanwhile is not put back by the mark.
	T.recordIngest(udb, &run, "https://gone.example", "Not listed")
	got, _ = loadCollection(udb, "alice", "col-a")
	if _, has := got.ArticlesFetched["Not listed"]; has {
		t.Error("a mark resurrected an article that is not on the list")
	}
}

// A contributor's run saves to the OWNER's record, not a stray copy in the
// contributor's own store that the owner never sees.
func TestAContributorsRunMarksTheOwnersRecord(t *testing.T) {
	store := stewardStores(t)
	alice, carol := store("alice"), store("carol")
	T := &OrchestrateApp{AppCore: AppCore{DB: CollectionsDB()}}
	c := Collection{ID: "col-a", Name: "Tax", Owner: "alice", AllowedUsers: []string{"carol"}, Contributors: []string{"carol"}}
	saveCollection(alice, c)
	T.recordIngest(carol, &c, "https://irs.example/p17.pdf", "")
	got, _ := loadCollection(alice, "alice", "col-a")
	if !collectionHasURL(got, "https://irs.example/p17.pdf") {
		t.Error("the owner's record did not get the contributor's fetch")
	}
	var stray Collection
	if carol.Get(CollectionsTable, "col-a", &stray) {
		t.Error("the contributor's store got a stray copy of the record")
	}
}

// Editing the list keeps where the entries that stay were fetched from, and
// forgets it for the ones taken off. Only the owner edits it.
func TestEditingTheListKeepsWhatStays(t *testing.T) {
	prevRoot, prevAuth, prevBase, prevVec := RootDB, AuthDB, orchestrateBaseDB, VectorDB
	t.Cleanup(func() { RootDB, AuthDB, orchestrateBaseDB, VectorDB = prevRoot, prevAuth, prevBase, prevVec })
	RootDB = &DBase{Store: kvlite.MemStore()}
	orchestrateBaseDB = RootDB
	VectorDB = &DBase{Store: kvlite.MemStore()}
	signedUp(t, "alice", "carol")
	saveCollection(UserDB(CollectionsDB(), "alice"), Collection{ID: "col-a", Owner: "alice", Name: "Tax",
		AllowedUsers: []string{"carol"}, Contributors: []string{"carol"},
		Articles:        []string{"Pub 17", "Pub 501"},
		ArticlesFetched: map[string]string{"Pub 17": "https://a.example", "Pub 501": "https://b.example"}})
	h := routed(&OrchestrateApp{AppCore: AppCore{DB: CollectionsDB()}})
	patch := func(user, body string) int {
		r := withSession(httptest.NewRequest("PATCH", "/api/collections/col-a", strings.NewReader(body)), user)
		w := httptest.NewRecorder()
		h(w, r)
		return w.Code
	}
	if code := patch("carol", `{"articles":["Mine"]}`); code != http.StatusForbidden {
		t.Errorf("a contributor rewrote the owner's list: %d", code)
	}
	if code := patch("alice", `{"articles":["Pub 17","Pub 970"]}`); code != http.StatusOK {
		t.Fatalf("the owner's edit failed: %d", code)
	}
	got, _ := loadCollection(UserDB(CollectionsDB(), "alice"), "alice", "col-a")
	if got.ArticlesFetched["Pub 17"] != "https://a.example" {
		t.Error("an entry that stayed forgot where it was fetched from")
	}
	if _, has := got.ArticlesFetched["Pub 501"]; has {
		t.Error("an entry taken off the list kept its mark")
	}
	if code := patch("alice", `{"articles":["`+strings.Repeat("x", maxArticleChars+1)+`"]}`); code != http.StatusBadRequest {
		t.Errorf("an over-long entry was taken: %d", code)
	}
}
