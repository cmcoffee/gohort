package core

import "testing"

// Ensure ClassifySource still distinguishes tiers — used as the safety
// filter for LLM-suggested domains in discoverAuthoritativeDomains.
func TestClassifySource_tiersForDomainDiscovery(t *testing.T) {
	cases := map[string]string{
		"https://europa.eu/":              "gov",
		"https://nist.gov/":               "gov",
		"https://www.ftc.gov/":            "gov",
		"https://www.who.int/":            "gov",
		"https://www.icrc.org/":           "ngo",
		"https://www.nature.com/":         "peer-reviewed",
		"https://pmc.ncbi.nlm.nih.gov/":   "peer-reviewed",
		"https://arxiv.org/":              "preprint",
		"https://stanford.edu/":           "edu",
		"https://www.mit.edu/":            "edu",
		"https://techcrunch.com/":         "web",
		"https://linkedin.com/":           "social",
		"https://example-vendor-blog.com/": "web",
	}
	for url, want := range cases {
		got := ClassifySource(url)
		if got != want {
			t.Errorf("ClassifySource(%q) = %q, want %q", url, got, want)
		}
	}
}
