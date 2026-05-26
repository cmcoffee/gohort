package core

import "strings"

// IsWeakSource returns true if a URL is unlikely to be a quality citation source.
// Used by source-collection paths to exclude vendor blogs, template sites,
// navigation pages, advocacy/commentary outlets, and content tiers that
// shouldn't compete with peer-reviewed / gov / edu material in the
// synthesis pool.
//
// Layered filter — earliest match wins:
//  1. Structural non-article URLs (search results, listings, view switchers).
//  2. Curated domain blocklist (vendor blogs, advocacy outlets, trade pubs).
//  3. Facebook group discussions (user-generated, not citable).
//  4. Navigation/index path patterns (/topics/, /about/, tag listings, etc.).
//  5. Generic PDF downloads with no article/paper/report context.
//  6. Tier-based drop via ClassifySource (blog, social, press release).
//
// Lives in its own file because source filtering is a distinct concern
// from search dispatch / classification in search.go: new blocklist
// entries and tier-rule tuning happen here without touching the rest
// of the search infrastructure.
func IsWeakSource(rawURL string) bool {
	if IsNonArticleURL(rawURL) {
		return true
	}

	lower := strings.ToLower(rawURL)

	// Vendor/product pages masquerading as content.
	weak_domains := []string{
		"aithor.com", // AI essay generator — LLM-generated content with no evidentiary value
		"lawinsider.com/contracts",
		"compliancequest.com",
		"salesforce.com/blog",
		"hubspot.com/blog",
		"simplywall.st", // stock analysis aggregator, not primary research
	}
	for _, d := range weak_domains {
		if strings.Contains(lower, d) {
			return true
		}
	}

	// Pure advocacy / vendor-marketing / industry-PR outlets. These are
	// different from the "commentary" tier (ideologically-charged but
	// substantive outlets handled by ClassifySource + synthesis
	// corroboration rules) — these are lobby fronts, product marketing,
	// clinic advertising, and press-release-as-news trade pubs with no
	// original reporting of their own. Block at the pool stage.
	advocacy_domains := []string{
		// Lobby-front and pure-advocacy orgs.
		"minimumwage.com",         // Employment Policies Institute anti-MW lobby site
		"vitaltransformation.com", // biopharma consultancy marketing
		// Industry trade publications (press-release-adjacent).
		"fiercebiotech.com",
		"fiercepharma.com",
		"drugdiscoverynews.com",
		"labiotech.eu",
		// Vendor marketing / product sites.
		"adheretech.com",
		"spirion.com",
		"fortifiedhealthsecurity.com",
		"hipaatimes.com",
		// Individual-clinic marketing blogs.
		"myfamilymd.org",
		"berelianimd.com",
	}
	for _, d := range advocacy_domains {
		if strings.Contains(lower, d) {
			return true
		}
	}

	// Facebook group discussions are user-generated commentary, not
	// citable sources. Official page posts (e.g., a senator's page)
	// are kept — those can be primary sources.
	if strings.Contains(lower, "facebook.com/groups/") {
		return true
	}

	// Navigation/index pages — not actual articles.
	weak_paths := []string{
		"/wiki_tags/", "/page/", "/tagged/",
		"/topics/", "/topic/", "/about-gsa",
		"/publications/2021", "/about/",
	}
	for _, p := range weak_paths {
		if strings.Contains(lower, p) {
			return true
		}
	}

	// Generic PDF downloads without specific article context.
	if strings.HasSuffix(lower, ".pdf") && !strings.Contains(lower, "article") && !strings.Contains(lower, "paper") && !strings.Contains(lower, "report") {
		return true
	}

	// Tier-based drop — press-release tier only. Blog and social are
	// allowed through the pool because genuine primary sources sometimes
	// publish on those platforms (e.g., a Substack by a subject-matter
	// expert, a researcher's Medium post). Synthesis discipline rules
	// de-prefer them against higher-tier sources. Press-release wires
	// are different: they are by definition not original reporting, and
	// their numbers travel pre-packaged from a PR desk — nothing
	// synthesis gains from including them.
	if ClassifySource(rawURL) == "press release" {
		return true
	}

	return false
}
