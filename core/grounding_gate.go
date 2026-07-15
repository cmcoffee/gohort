// Grounding gate — a CONDITIONAL post-answer check, deliberately NOT an
// always-on second pass. Before the agent loop returns a final answer it scans
// that answer for money figures ($1,299, USD 200, 50 euros) and checks each
// against the text the model was actually handed this conversation: tool
// results and the user's own messages. A figure present in NEITHER was supplied
// from the model's weights — precisely the price-fabrication failure the worker
// keeps hitting. The gate then injects ONE corrective re-prompt telling the
// model to look the figure up or drop it, and lets the loop run another round
// (so it can actually web_search). It does nothing on turns with no figures,
// and by construction never fires for an agent with no tools (the loop guards
// on len(tools) > 0), since "look it up" is impossible there — that no-tool
// case is exactly the always-on-self-verify trap we are avoiding: a model
// re-checking a fabricated number against its own weights just launders it.
//
// SCOPE: TAGGED FIGURES ONLY — a number carrying a signal that it's a factual
// claim, not a count or index. Three categories today: money (currency-tagged),
// percentages ("40%"), and magnitudes ("2.3 million"). A BARE integer is never
// flagged — "3 options", "step 2", "48 ports", "in 2024", a version number are
// the noise floor, and a gate that nags on those gets switched off on day one.
// Unit-tagged measurements (kg, GB, mph) are the next category but need a
// curated unit list (some units are ambiguous English words: "in", "m", "s"),
// so they're deferred rather than added imprecisely. Names, dates, versions, and
// IDs stay out — extracting them without false flags needs more than a regex.
// The seams here (figurePatterns / unsourcedFigures / groundingCorpus)
// generalize to a new category by adding one high-precision pattern.
//
// Ships DARK: gated off by default via the tune_grounding_gate knob (0 = off,
// 1 = on) so it can be turned on per-deployment once proven on real turns.

package core

import (
	"regexp"
	"strings"
)

const tuneGroundingGate = "tune_grounding_gate"

// groundingGateMarker prefixes the gate's own corrective re-prompt so
// groundingCorpus can EXCLUDE it. Without this, the corrective (which names the
// figures) would re-enter the corpus as a user message, and a model that
// ignored the instruction and simply restated the number would see it counted
// as "sourced" on the next pass — a false clear. The marker keeps the gate
// honest: only a real tool result or a real user message ever grounds a figure.
const groundingGateMarker = "​[grounding-gate]"

func init() {
	RegisterTunable(TunableSpec{
		Key:      tuneGroundingGate,
		Category: "Limits",
		Label:    "Grounding gate: unsourced figures",
		Help:     "When on, a final answer stating a tagged figure (a price, a percentage, or a magnitude like \"2.3 million\") that appears in neither this conversation's tool results nor the user's messages triggers one corrective re-prompt to look it up or drop it. Bare counts and years are ignored.",
		Kind:     KindBool,
		Default:  0,
		Min:      0,
		Max:      1,
	})
}

// GroundingGateEnabled reports whether the money-figure grounding gate is on.
func GroundingGateEnabled() bool { return TuneBool(tuneGroundingGate) }

// figurePatterns matches only TAGGED numbers — a number carrying a signal that
// it's a factual claim, not a count or an index. Bare integers are deliberately
// NOT matched (they're "3 options", "step 2", "48 ports", "in 2024", version
// numbers — the noise floor that would make the gate nag every turn and get it
// switched off). Each pattern is a high-precision category:
//   - money:     currency symbol ($ € £ ¥), trailing code/word, or leading code
//   - percent:   a number with "%" or "percent"
//   - magnitude: a number with a scale word (thousand/million/billion/trillion)
// Unit-tagged measurements (kg, GB, mph) are a deliberate future category — they
// need a curated unit list because some units are ambiguous English words ("in",
// "m", "s"), so they're left out of this cut rather than added imprecisely.
var figurePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)[$€£¥]\s?\d[\d,]*(?:\.\d+)?|\b\d[\d,]*(?:\.\d+)?\s?(?:dollars|euros|pounds|cents|usd|eur|gbp|jpy)\b|\b(?:usd|eur|gbp|jpy)\s?\d[\d,]*(?:\.\d+)?`),
	regexp.MustCompile(`(?i)\b\d[\d,]*(?:\.\d+)?\s?(?:%|percent)\b|\d[\d,]*(?:\.\d+)?%`),
	regexp.MustCompile(`(?i)\b\d[\d,]*(?:\.\d+)?\s?(?:thousand|million|billion|trillion)\b`),
}

// nonDigit strips everything but digits so "$3,000", "3000", and "3,000" all
// reduce to the same core for comparison.
var nonDigit = regexp.MustCompile(`[^\d]`)

func digitsOnly(s string) string { return nonDigit.ReplaceAllString(s, "") }

// unsourcedFigures returns the distinct tagged figures in answer whose digit-core
// appears nowhere in sourced. A core shorter than 2 digits is skipped: a lone
// "$5" or "5%" matches almost any text and isn't the multi-digit fabrication the
// gate targets. Comparison is on digits only, so a currency symbol, "%", a scale
// word, or thousands separators never cause a spurious miss.
func unsourcedFigures(answer, sourced string) []string {
	type fig struct {
		text  string
		start int
	}
	var figs []fig
	for _, re := range figurePatterns {
		for _, loc := range re.FindAllStringIndex(answer, -1) {
			figs = append(figs, fig{answer[loc[0]:loc[1]], loc[0]})
		}
	}
	if len(figs) == 0 {
		return nil
	}
	sourcedDigits := digitsOnly(sourced)
	seen := map[string]bool{}
	var out []string
	for _, f := range figs {
		core := digitsOnly(f.text)
		if len(core) < 2 {
			continue
		}
		if strings.Contains(sourcedDigits, core) {
			continue // the figure's digits appear in a real source
		}
		// Skip PERCENT / MAGNITUDE figures the model already HEDGED ("about
		// 80%", "~5%", "roughly a million"): an explicit estimate is the honest
		// behavior the gate wants to encourage, not a fabrication to catch.
		// MONEY is exempt — a hedged price ("about $300") is still a price the
		// user may act on, so it stays gated. Only the short run of text right
		// before the figure is inspected, so a hedge earlier in the sentence
		// doesn't excuse a later precise claim.
		if !figurePatterns[0].MatchString(f.text) {
			lo := f.start - 28
			if lo < 0 {
				lo = 0
			}
			if figureHedgeRE.MatchString(answer[lo:f.start]) {
				continue
			}
		}
		key := strings.TrimSpace(f.text)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, key)
	}
	return out
}

// figureHedgeRE matches an estimate marker sitting immediately before a figure
// ("about ", "roughly ", "~", "up to ", "give or take "). When the model has
// already flagged the number as approximate, that's the honest hedge the gate
// wants to encourage, so the figure is left alone rather than re-prompted.
var figureHedgeRE = regexp.MustCompile(`(?i)(?:about|around|roughly|approximately|approx\.?|nearly|almost|circa|up to|as (?:much|many) as|on the order of|order of|ballpark|give or take|somewhere (?:around|near|between)|~)\s*$`)

// groundingCorpus concatenates every tool result and every user message in the
// working history — the text the model was actually handed this conversation.
// Assistant turns are excluded on purpose: the model's own prior prose is not a
// source, and counting it would let a fabricated figure launder itself by being
// repeated. Errored tool results and the gate's own corrective messages are
// also excluded (an error carries no reliable figure; the corrective is not a
// source).
func groundingCorpus(history []Message) string {
	var b strings.Builder
	for _, m := range history {
		if m.Role == "user" && !strings.Contains(m.Content, groundingGateMarker) {
			b.WriteString(m.Content)
			b.WriteByte('\n')
		}
		for _, tr := range m.ToolResults {
			if !tr.IsError {
				b.WriteString(tr.Content)
				b.WriteByte('\n')
			}
		}
	}
	return b.String()
}

// groundingGatePrompt builds the corrective re-prompt naming the unsourced
// figures. Prefixed with groundingGateMarker so groundingCorpus skips it.
func groundingGatePrompt(figs []string) string {
	quoted := make([]string, len(figs))
	for i, f := range figs {
		quoted[i] = "\"" + f + "\""
	}
	noun := "the figure " + quoted[0]
	if len(figs) > 1 {
		noun = "the figures " + strings.Join(quoted, ", ")
	}
	return groundingGateMarker + " Your reply states " + noun +
		", but that does not appear in any tool result or in what the user told you this conversation, so it can only be coming from memory, and a figure you cannot point to a source for is a guess. Do ONE of these now: call web_search or fetch_url to get the real figure and quote what the result returns, or remove the figure and say plainly you don't have a sourced number and can look it up. Do not restate it from memory."
}
