package core

import (
	"context"
	"fmt"
	"strings"
)

// SourceCandidate is a single source under consideration for relevance
// bucketing. Title and Snippet should be short — the bucketer truncates
// long values to keep the prompt manageable.
type SourceCandidate struct {
	Title   string
	Snippet string
}

// RelevanceBuckets holds source indices grouped by relevance tier. The
// indices refer back to the candidates slice passed into the bucketer.
type RelevanceBuckets struct {
	Strong   []int `json:"strong"`
	Moderate []int `json:"moderate"`
	Weak     []int `json:"weak"`
}

// BucketCandidatesByRelevance asks the worker LLM to categorize each
// candidate as STRONG, MODERATE, or WEAK based on subject relevance to
// the supplied contextDesc. Returns indices reordered strong → moderate
// → weak, with anything the LLM forgot to bucket appended at the end so
// no candidate is silently dropped.
//
// contextDesc is interpolated verbatim into the prompt — provide it as a
// short labeled block, e.g.:
//
//	`Question: "How does X work?"`
//
// labelPrefix is used in debug logs to distinguish which call site
// produced the bucketing.
//
// On error or parse failure, returns the original index order so the
// caller never loses candidates.
func BucketCandidatesByRelevance(
	agent *AppCore,
	ctx context.Context,
	contextDesc string,
	candidates []SourceCandidate,
	labelPrefix string,
) []int {
	if len(candidates) == 0 {
		return nil
	}
	if len(candidates) <= 5 {
		// Below the threshold, ranking is pointless — keep them all in order.
		out := make([]int, len(candidates))
		for i := range candidates {
			out[i] = i
		}
		return out
	}

	var listing strings.Builder
	for i, c := range candidates {
		title := c.Title
		if len(title) > 120 {
			title = title[:120]
		}
		snippet := c.Snippet
		if len(snippet) > 200 {
			snippet = snippet[:200]
		}
		fmt.Fprintf(&listing, "%d. %s\n   %s\n", i, title, snippet)
		// Cap input size — the model can only process so many in one call.
		if i >= 60 {
			break
		}
	}

	resp, err := agent.WorkerChat(ctx, []Message{
		{Role: "user", Content: fmt.Sprintf(`Bucket these sources into STRONG, MODERATE, and WEAK matches based on subject relevance.

%s

Sources:
%s
BUCKET DEFINITIONS:
- STRONG: directly addresses the subject with concrete evidence. Peer-reviewed papers, authoritative reports, primary sources on the actual topic. A researcher would absolutely cite this.
- MODERATE: relevant but indirect — adjacent topic, partial coverage, or commentary rather than primary evidence. Might be cited for context.
- WEAK: shares vocabulary but not subject. Tangential, off-topic, vendor docs, glossaries, generic overviews of a broader field, or things that just happen to mention the keywords.

Examples of WEAK matches:
- Civil engineering "structural instability" docs for an AI instability question
- Generic business AI articles for a military AI question
- Unrelated political news for a climate policy question
- General pharmacology overviews for a specific drug question
- Vendor product pages, software help, glossary entries

SCOPE MISMATCH — a source that matches the topic's vocabulary but addresses a DIFFERENT scope is WEAK, not MODERATE. Scope means country/region, named entity, and time period. Check the title and snippet for scope signals before bucketing. Common anti-patterns:
- Wrong country: a Pakistani election-watchdog article ("FAFEN: Villages Record Higher Voter Turnout Than Urban Centers") for a US-elections question. Wrong country — bucket WEAK even though the vocabulary "voter turnout rural urban" matches.
- Wrong entity: a "Blue Moon Metals" mining-company filing for a question about Blue Origin's "Blue Moon" lunar lander. Name collision, different entity — WEAK.
- Wrong time period: a 2012 study about a policy regime that ended in 2018, cited for a 2026 policy question. Stale scope — WEAK unless the snippet says it's still current.
- Wrong subtopic: an environmental-science journal article matched by a political-science question because both mention "climate change." Different research community — WEAK.

If the context description above names a specific country (US, UK, etc.), specific entity, or specific time period, treat scope mismatch as WEAK even if the subject vocabulary matches perfectly.

Every source must be in exactly ONE bucket. Place all source numbers somewhere — do not omit any.

Reply with ONLY a JSON object (no markdown fencing):
{"strong": [3, 7], "moderate": [0, 2, 5], "weak": [1, 4, 6, 8, 9]}`, contextDesc, listing.String())},
	},
		WithSystemPrompt("Bucket sources into strong/moderate/weak by SUBJECT relevance, not vocabulary overlap. Reply with ONLY the JSON object."),
		WithMaxTokens(512),
		WithThink(true))
	if err != nil {
		Debug("[%s] relevance bucket call failed: %s", labelPrefix, err)
		return identityOrder(len(candidates))
	}

	var buckets RelevanceBuckets
	if DecodeJSON(ResponseText(resp), &buckets) != nil {
		Debug("[%s] relevance bucket parse failed", labelPrefix)
		return identityOrder(len(candidates))
	}
	if len(buckets.Strong)+len(buckets.Moderate)+len(buckets.Weak) == 0 {
		Debug("[%s] relevance buckets empty", labelPrefix)
		return identityOrder(len(candidates))
	}

	Debug("[%s] relevance buckets: strong=%d moderate=%d weak=%d (of %d candidates)",
		labelPrefix, len(buckets.Strong), len(buckets.Moderate), len(buckets.Weak), len(candidates))
	for _, idx := range buckets.Weak {
		if idx >= 0 && idx < len(candidates) {
			Debug("[%s] WEAK: %s", labelPrefix, candidates[idx].Title)
		}
	}

	seen := make(map[int]bool, len(candidates))
	order := make([]int, 0, len(candidates))
	add := func(indices []int) {
		for _, idx := range indices {
			if idx >= 0 && idx < len(candidates) && !seen[idx] {
				order = append(order, idx)
				seen[idx] = true
			}
		}
	}
	add(buckets.Strong)
	add(buckets.Moderate)
	add(buckets.Weak)
	// Anything the LLM forgot to bucket lands at the end so we don't lose it.
	for i := range candidates {
		if !seen[i] {
			order = append(order, i)
		}
	}
	return order
}

// identityOrder returns the indices [0, 1, ..., n-1] — used as a safe
// fallback when bucketing fails.
func identityOrder(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i
	}
	return out
}

// BucketCandidatesByRelevanceDetailed is like BucketCandidatesByRelevance
// but returns the full RelevanceBuckets structure instead of a flat
// ordered index list. Callers that need to distinguish strong/moderate/
// weak tiers for tier-aware filtering (e.g., dropping weak entirely when
// the strong+moderate pool is large enough) should use this variant.
// Returns nil on failure so callers can fall back to keeping all
// candidates unchanged.
func BucketCandidatesByRelevanceDetailed(
	agent *AppCore,
	ctx context.Context,
	contextDesc string,
	candidates []SourceCandidate,
	labelPrefix string,
) *RelevanceBuckets {
	if len(candidates) == 0 {
		return nil
	}
	if len(candidates) <= 5 {
		// Below the threshold, ranking is pointless — treat all as strong
		// so the caller keeps them all.
		b := &RelevanceBuckets{}
		for i := range candidates {
			b.Strong = append(b.Strong, i)
		}
		return b
	}

	var listing strings.Builder
	for i, c := range candidates {
		title := c.Title
		if len(title) > 120 {
			title = title[:120]
		}
		snippet := c.Snippet
		if len(snippet) > 200 {
			snippet = snippet[:200]
		}
		fmt.Fprintf(&listing, "%d. %s\n   %s\n", i, title, snippet)
		if i >= 60 {
			break
		}
	}

	resp, err := agent.WorkerChat(ctx, []Message{
		{Role: "user", Content: fmt.Sprintf(`Bucket these sources into STRONG, MODERATE, and WEAK matches based on subject relevance.

%s

Sources:
%s
BUCKET DEFINITIONS:
- STRONG: directly addresses the subject with concrete evidence. Peer-reviewed papers, authoritative reports, primary sources on the actual topic. A researcher would absolutely cite this.
- MODERATE: relevant but indirect — adjacent topic, partial coverage, or commentary rather than primary evidence. Might be cited for context.
- WEAK: shares vocabulary but not subject. Tangential, off-topic, vendor docs, glossaries, generic overviews of a broader field, things that just happen to mention the keywords, or content that lives on an authoritative domain (.gov, .edu, peer-reviewed journal) but addresses an unrelated topic (e.g., an FTC consumer alert about online quiz scams in an AI investment research question).

Examples of WEAK matches:
- Civil engineering "structural instability" docs for an AI instability question
- Generic business AI articles for a military AI question
- Unrelated political news for a climate policy question
- General pharmacology overviews for a specific drug question
- Vendor product pages, software help, glossary entries
- Topically unrelated content on a .gov or .edu domain — the domain is authoritative but the content doesn't address the research question

Every source must be in exactly ONE bucket. Place all source numbers somewhere — do not omit any. Be aggressive in marking off-topic sources as WEAK even if they live on authoritative domains — a government search page or an unrelated consumer alert is still WEAK if the content doesn't match the research question.

Reply with ONLY a JSON object (no markdown fencing):
{"strong": [3, 7], "moderate": [0, 2, 5], "weak": [1, 4, 6, 8, 9]}`, contextDesc, listing.String())},
	},
		WithSystemPrompt("Bucket sources into strong/moderate/weak by SUBJECT relevance, not vocabulary overlap or domain authority. Mark off-topic content WEAK even on authoritative domains. Reply with ONLY the JSON object."),
		WithMaxTokens(512),
		WithThink(true))
	if err != nil {
		Debug("[%s] relevance bucket call failed: %s", labelPrefix, err)
		return nil
	}

	var buckets RelevanceBuckets
	if DecodeJSON(ResponseText(resp), &buckets) != nil {
		Debug("[%s] relevance bucket parse failed", labelPrefix)
		return nil
	}
	if len(buckets.Strong)+len(buckets.Moderate)+len(buckets.Weak) == 0 {
		Debug("[%s] relevance buckets empty", labelPrefix)
		return nil
	}

	Debug("[%s] relevance buckets: strong=%d moderate=%d weak=%d (of %d candidates)",
		labelPrefix, len(buckets.Strong), len(buckets.Moderate), len(buckets.Weak), len(candidates))
	for _, idx := range buckets.Weak {
		if idx >= 0 && idx < len(candidates) {
			Debug("[%s] WEAK: %s", labelPrefix, candidates[idx].Title)
		}
	}

	return &buckets
}
