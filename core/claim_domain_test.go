// Being told something is not the same as knowing it.
//
// MemSource recorded WHO put a claim into memory and nothing recorded whether
// that settled it, so "craig prefers snake_case" and "the database is on NFS"
// were stored identically and recalled identically. The second is checkable
// independently of who said it, and rendered flat in a prompt it is
// indistinguishable from something the agent verified — which is how a passing
// remark becomes a fact quoted back as established.
package core

import (
	"strings"
	"testing"
	"time"
)

func told(note string, dom ClaimDomain, asOf time.Time) MemoryFact {
	return MemoryFact{
		Note:             note,
		MemoryProvenance: MemoryProvenance{Source: MemSourceUserStated, Domain: dom, AsOf: asOf},
	}
}

// What someone says about themselves is settled by their saying it.
func TestSpeakerIsAuthoritativeAboutThemselves(t *testing.T) {
	if !SpeakerAuthoritative(MemSourceUserStated, ClaimSelf) {
		t.Error("a person is the authority on their own preferences")
	}
	for _, c := range []struct {
		src MemSource
		dom ClaimDomain
	}{
		{MemSourceUserStated, ClaimWorld},
		{MemSourceUserStated, ClaimDomainUnknown},
		{MemSourceInferred, ClaimSelf},
		{MemSourceRetrieved, ClaimSelf},
	} {
		if SpeakerAuthoritative(c.src, c.dom) {
			t.Errorf("src=%v dom=%v must not settle a claim by assertion", c.src, c.dom)
		}
	}
}

// Only the told world-claim needs naming. Hedging a preference reads as
// doubting the person who holds it.
func TestOnlyToldWorldClaimsAreAttributed(t *testing.T) {
	if !NeedsAttribution(MemSourceUserStated, ClaimWorld) {
		t.Error("a told world-claim must say it was told")
	}
	if NeedsAttribution(MemSourceUserStated, ClaimSelf) {
		t.Error("a preference must not be hedged")
	}
	if NeedsAttribution(MemSourceRetrieved, ClaimWorld) {
		t.Error("a retrieved claim was checked at save time; it is not hearsay")
	}
}

// Grounding asks a different question than merge, so it orders differently: a
// told world-claim sits below anything a tool actually saw.
func TestGroundingRanksCheckedAboveTold(t *testing.T) {
	toldWorld := ClaimAuthority(MemSourceUserStated, ClaimWorld)
	if got := ClaimAuthority(MemSourceRetrieved, ClaimWorld); got <= toldWorld {
		t.Errorf("retrieved (%d) must outrank a told world-claim (%d) for grounding", got, toldWorld)
	}
	if got := ClaimAuthority(MemSourceObserved, ClaimWorld); got <= toldWorld {
		t.Errorf("observed (%d) must outrank a told world-claim (%d)", got, toldWorld)
	}
	// And the merge ordering is deliberately NOT changed — supersession still
	// ranks a human's entry above a machine's.
	if SourceTrust(MemSourceUserStated) <= SourceTrust(MemSourceRetrieved) {
		t.Error("SourceTrust must keep ranking a human entry above a retrieved one for merges")
	}
}

// An unclassified legacy row must not acquire authority it was never granted.
func TestUnknownDomainGrantsNoAuthority(t *testing.T) {
	if SpeakerAuthoritative(MemSourceUserStated, ClaimDomainUnknown) {
		t.Error("unclassified is not evidence of authority")
	}
}

func TestRecallAttributesAToldWorldClaim(t *testing.T) {
	when := time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC)
	block := RenderMemoryFactsBlock([]MemoryFact{
		told("the production database sits on NFS", ClaimWorld, when),
		told("prefers snake_case", ClaimSelf, when),
	})
	if !strings.Contains(block, "the production database sits on NFS (told to you on 2026-08-04, not independently checked)") {
		t.Errorf("a told world-claim should be attributed with its date, got:\n%s", block)
	}
	if strings.Contains(block, "prefers snake_case (told") {
		t.Errorf("a preference must render flat, got:\n%s", block)
	}
}

// A row with no AsOf still gets attributed — the date is the detail, being
// told is the point.
func TestAttributionSurvivesAMissingDate(t *testing.T) {
	block := RenderMemoryFactsBlock([]MemoryFact{told("the API returns ISO dates", ClaimWorld, time.Time{})})
	if !strings.Contains(block, "(told to you, not independently checked)") {
		t.Errorf("attribution should not depend on a date being recorded, got:\n%s", block)
	}
}

// The grounding corpus keeps told world-claims — the agent does know them —
// but keeps them marked, or including them licenses asserting them as checked.
func TestSourcedCorpusMarksRatherThanDrops(t *testing.T) {
	corpus := SourcedFactCorpus([]MemoryFact{
		told("the cluster has three nodes", ClaimWorld, time.Time{}),
		{Note: "release notes list v2.1", MemoryProvenance: MemoryProvenance{Source: MemSourceRetrieved, Domain: ClaimWorld}},
		{Note: "guessed at the node count", MemoryProvenance: MemoryProvenance{Source: MemSourceInferred, Domain: ClaimWorld}},
	})
	if !strings.Contains(corpus, "the cluster has three nodes (told to you") {
		t.Errorf("a told world-claim should stay, marked; got:\n%s", corpus)
	}
	if !strings.Contains(corpus, "release notes list v2.1\n") || strings.Contains(corpus, "release notes list v2.1 (told") {
		t.Errorf("a retrieved claim should stay unmarked; got:\n%s", corpus)
	}
	if strings.Contains(corpus, "guessed at the node count") {
		t.Errorf("an inferred claim is not a grounding source; got:\n%s", corpus)
	}
}

// Volatility and attribution are separate axes and must both survive.
func TestAttributionAndVolatilityCompose(t *testing.T) {
	f := told("the staging box runs 22.04", ClaimWorld, time.Now())
	f.Volatility = VolVolatile
	got := RenderMemoryFactsBlock([]MemoryFact{f})
	if !strings.Contains(got, "told to you") || !strings.Contains(got, "volatile") {
		t.Errorf("both markers should render, got:\n%s", got)
	}
}

// The path that actually launders is the model writing down what it heard:
// store_fact records those as OBSERVED, so scoping attribution to user_stated
// would have missed every one of them.
func TestObservedWorldClaimsAreAttributedToo(t *testing.T) {
	if !NeedsAttribution(MemSourceObserved, ClaimWorld) {
		t.Error("a world-claim the model recorded from conversation is not checked either")
	}
	if NeedsAttribution(MemSourceObserved, ClaimSelf) {
		t.Error("a preference heard in conversation still must not be hedged")
	}
	// Retrieved and imported came from a source at save time.
	if NeedsAttribution(MemSourceImported, ClaimWorld) {
		t.Error("an imported claim came from a store, not from conversation")
	}
}

// The marker must say how the claim actually arrived — blaming the user for the
// model's own inference is its own wrong record.
func TestAttributionPhraseMatchesTheOrigin(t *testing.T) {
	cases := map[MemSource]string{
		MemSourceUserStated: "told to you",
		MemSourceInferred:   "your own inference",
		MemSourceObserved:   "recorded from conversation",
	}
	for src, want := range cases {
		f := MemoryFact{Note: "the cluster has three nodes",
			MemoryProvenance: MemoryProvenance{Source: src, Domain: ClaimWorld}}
		got := RenderMemoryFactsBlock([]MemoryFact{f})
		if !strings.Contains(got, "("+want+", not independently checked)") {
			t.Errorf("source %v should render %q, got:\n%s", src, want, got)
		}
	}
}

// --- the classifier -------------------------------------------------------

func TestClassifierReadsSelfClaims(t *testing.T) {
	for _, note := range []string{
		"User prefers metric units",
		"Time zone is America/Los_Angeles",
		"works at Acme Corp",
		"user is vegetarian",
		"goes by Robin",
		"wants shorter answers with no preamble",
	} {
		if got := classifyClaimDomain(note); got != ClaimSelf {
			t.Errorf("%q classified %v, want self", note, got)
		}
	}
}

func TestClassifierReadsWorldClaims(t *testing.T) {
	for _, note := range []string{
		"production API needs JWT in the X-Auth header",
		"the staging server runs Ubuntu 22.04",
		"the cluster has three nodes",
		"the database schema has a users table",
		"the library version is 2.1",
	} {
		if got := classifyClaimDomain(note); got != ClaimWorld {
			t.Errorf("%q classified %v, want world", note, got)
		}
	}
}

// The two vocabularies overlap constantly, and when they do the preference is
// what the note asserts.
func TestPreferenceWinsOverTheSystemItNames(t *testing.T) {
	if got := classifyClaimDomain("prefers the API to return JSON"); got != ClaimSelf {
		t.Errorf("a preference that names a system is still a preference, got %v", got)
	}
	// The same subject, asserted rather than preferred.
	if got := classifyClaimDomain("the API returns JSON"); got != ClaimWorld {
		t.Errorf("an assertion about the API is a world-claim, got %v", got)
	}
}

// Unknown is a real answer: a false self grants authority nobody earned, and a
// false world prints "not independently checked" against someone's preference.
func TestClassifierDeclinesWhenUnsure(t *testing.T) {
	for _, note := range []string{"Atlas", "mid-June", "follow up on Thursday"} {
		if got := classifyClaimDomain(note); got != ClaimDomainUnknown {
			t.Errorf("%q classified %v, want unknown", note, got)
		}
	}
}

// A writer that knows beats any keyword list.
func TestDeclarationBeatsTheClassifier(t *testing.T) {
	// The note reads as a world-claim; the caller says it is about the user.
	if got := domainFor(FactWritePolicy{Domain: ClaimSelf}, "the server is down"); got != ClaimSelf {
		t.Errorf("an explicit declaration must win, got %v", got)
	}
	if got := domainFor(FactWritePolicy{}, "the server is down"); got != ClaimWorld {
		t.Errorf("with no declaration the classifier decides, got %v", got)
	}
}

// End to end: a world-claim stored through the ordinary path comes back marked.
func TestStoredWorldClaimRecallsAttributed(t *testing.T) {
	db := memDB(t)
	StoreMemoryFactP(db, "ns", "the staging server runs Ubuntu 22.04", FactWritePolicy{Source: MemSourceObserved})
	StoreMemoryFactP(db, "ns", "User prefers metric units", FactWritePolicy{Source: MemSourceObserved})

	block := RenderMemoryFactsBlock(ListMemoryFacts(db, "ns"))
	if !strings.Contains(block, "Ubuntu 22.04 (recorded from conversation") {
		t.Errorf("a stored world-claim should recall attributed, got:\n%s", block)
	}
	if strings.Contains(block, "metric units (recorded") {
		t.Errorf("a preference must recall flat, got:\n%s", block)
	}
}
