package core

// BannedWordsRule is applied to LLM stages so AI-tell words never
// appear in output.
const BannedWordsRule = `
BANNED WORDS — NEVER USE THESE:
- "demonstrably" — state the evidence and let the reader judge
- "underscores" / "highlights" / "reflects" — name the relationship directly
- "it's worth noting" / "notably" — if it's worth noting, just state it
- "landscape" (as metaphor) — say "market", "field", "situation", or be specific
- "leverage" (as verb) — say "use"
- "delve" / "delve into" — say "examine" or just do it
`

// TimeAwarenessRule guards against stale projections in LLM output.
const TimeAwarenessRule = `
TIME AWARENESS:
- Check today's date at the top of this prompt. If a source says something "is forecast" or "will reach $X by YEAR" and YEAR has already passed, DO NOT repeat that phrasing. Rewrite in past tense ("was projected to reach", "earlier forecasts anticipated") or find a more current figure from another source.
- Never present past-dated projections as if they were still future. "Will reach $1.5 trillion in 2025" written in 2026 is a tell that you copied from stale source material without checking the date.
- For genuinely future years (current year + 1 or later), future tense is fine.
- If a forecast period is more than 1 year in the past and no actual result is given, the figure is stale — soften the claim or omit it.
` + BannedWordsRule
