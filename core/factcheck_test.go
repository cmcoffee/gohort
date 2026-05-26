package core

import (
	"strings"
	"testing"
)

func TestVerifyFacts_catchesFabricatedNumbers(t *testing.T) {
	parents := []string{
		"The China AI market is projected to reach $210 billion by 2035, according to Market Research Future.",
		"Gartner forecasts worldwide AI spending of $2.52 trillion in 2026.",
		"Over 80% of corporate AI pilot projects fail to deliver value.",
	}

	// This output has two fabricated numbers and one legitimate one.
	output := `Global AI spending is projected to reach $2.52 trillion in 2026,
with the China AI market growing to $4041.48 million by 2035. Another forecast
suggests $3.497.26 trillion by 2033. Pilot project failure rates exceed 80%.`

	rejected := VerifyFacts(output, parents)

	var flagged []string
	for _, r := range rejected {
		flagged = append(flagged, r.Normalized)
	}

	mustFlag := []string{"4041.48", "3.497.26"}
	for _, want := range mustFlag {
		found := false
		for _, f := range flagged {
			if strings.Contains(f, want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected fact-check to flag %q, but did not (flagged: %v)", want, flagged)
		}
	}

	// Should NOT flag the legitimate $2.52 trillion or the 80% rate.
	for _, r := range rejected {
		if strings.Contains(r.Normalized, "2.52") {
			t.Errorf("false positive: flagged legitimate number %q", r.Raw)
		}
		if r.Normalized == "80%" {
			t.Errorf("false positive: flagged legitimate percentage %q", r.Raw)
		}
	}
}

// Regression test for the fabricated ethical-AI breakdown from run
// 156987e3. The worker LLM invented specific corporate spending figures
// ($210M bias detection, $150M privacy tech, etc.) and cited them to
// UNESCO. With the unit-aware extractor, all 15 of these numbers should
// be flagged when the haystack contains no matching "<digits><unit>"
// tokens. The old substring-match version only caught 7 of 15 because
// small numbers like $90M matched "90%" or "90 billion" substrings.
func TestVerifyFacts_unitAwareCatchesAllFabricatedMillions(t *testing.T) {
	parents := []string{
		// Haystack has unrelated content with numbers that would have
		// produced false negatives under the old substring approach:
		// a 90% figure and a 50 billion figure both contain the digits
		// that appear in the fabricated $90M and $50M claims below.
		"Public trust in AI sits at around 90% in recent polls.",
		"Venture capital for AI startups exceeded $50 billion in 2024.",
		"The specific breakdown of Ethical AI spending by company is not publicly disclosed.",
	}
	output := `Google invested $210 million in bias detection tools, $150 million in privacy-enhancing technologies, and $90 million in AI ethics training.
Microsoft invested $180 million, $120 million, and $75 million respectively.
Meta invested $160 million, $100 million, and $60 million.
Amazon invested $140 million, $90 million, and $50 million.
OpenAI invested $100 million, $70 million, and $40 million.`

	rejected := VerifyFacts(output, parents)

	// Every fabricated million should be caught. Old substring code
	// caught only 210/180/160/140/150/120/100 = 7 because 90/75/60/50/
	// 70/40 matched substrings. Unit-aware matching catches all of
	// them because "90M" != "90%" and "50M" != "50B".
	wantNormalized := []string{
		"210M", "180M", "160M", "140M", "100M", // bias detection
		"150M", "120M", "100M", "90M", "70M", // privacy tech
		"90M", "75M", "60M", "50M", "40M", // training
	}
	flagged := make(map[string]bool)
	for _, r := range rejected {
		flagged[r.Normalized] = true
	}
	for _, want := range wantNormalized {
		if !flagged[want] {
			t.Errorf("expected fact-check to flag %q, but did not", want)
		}
	}

	// Should NOT flag the 90% in the haystack.
	if flagged["90%"] {
		t.Errorf("false positive: 90%% should match haystack")
	}
}
