// The clauses the core agent loop appends, registered so they can be seen.
//
// core/agent_loop.go assembles every agent's system prompt, and until now the
// behaviour blocks it appends were plain string concatenation: twelve
// paragraphs, several of them encoding an incident, that no surface could show
// and no operator could switch off. The Prompts page listed ten registered
// blocks and omitted these, with nothing saying so — which is worse than
// listing none, because a partial list reads as complete. An operator asking
// "what are my agents told?" got a confident, wrong answer.
//
// ONE STRING, NOT TWO. The registry and the assembler read the same const.
// Registering a display copy of the text would buy the page a listing and
// guarantee it drifts from what is actually sent, which is the failure this
// exists to prevent — so each clause is a const here, the block registration
// names it, and the accessor hands the assembler EffectiveBlockText of the same
// key. An operator's edit changes what agents receive; a disable stops the
// append.
//
// PLACEHOLDERS, NOT FORMAT VERBS. Two clauses are not fixed text: the volatile
// facts block names the web tools only when the catalogue has one, and the
// round budget carries the turn's number. Both take a named placeholder
// ({lookup}, {rounds}) expanded by string replacement rather than a %s or %d,
// for two reasons: the page shows the shape that is actually sent instead of a
// format string, and an override that drops or duplicates a verb degrades to a
// missing substitution rather than a mangled "%!d(MISSING)" clause.
//
// Gates are the real condition, not a category. A rule present on some turns
// and absent on others is exactly what made these hard to reason about.

package prompts

import (
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/cmcoffee/snugforge/nfo"
)

// Keys for the core loop's behaviour blocks, exported so a surface can address
// one (and so the assembler and registry cannot disagree about a spelling).
const (
	AnsweringRoundsKey   = "framework.answering_rounds"
	GroundingContractKey = "framework.grounding_contract"
	CapabilityFirstKey   = "framework.capability_first"
	GroundingKey         = "framework.grounding"
	ActionsKey           = "framework.actions"
	DisagreeingKey       = "framework.disagreeing"
	NumbersKey           = "framework.numbers"
	NoFalsePrecisionKey  = "framework.no_false_precision"
	VolatileFactsKey     = "framework.volatile_facts"
	SecretsKey           = "framework.secrets"
	InternalMarkersKey   = "framework.internal_markers"
	RoundBudgetKey       = "framework.round_budget"
)

// The text, verbatim as the loop sends it.
const (
	answeringRoundsRule = "[Answering across tool rounds: NEVER emit answer text in the same step as a tool call. Call your tools FIRST, wait for the results, THEN write your answer from those results, in a final step that has no tool call. Do NOT pre-write an answer from training/memory and then also fetch tools: an answer composed before the results is exactly the double-emit to avoid. Answer after tools, not before. A brief progress note as you work (\"checking that now…\") is fine; your actual answer waits for the end and appears exactly once. Never two answers in one turn.]"

	groundingContractRule = "[Grounding contract: the blocks below are one rule seen from several sides. You earn the right to state a fact by pointing to where it came from THIS turn; when you can't, say so instead of guessing.]"

	capabilityFirstRule = "[Capability-first: when a tool or agent can do the job or get fresher information than you hold (news, prices, status, anything that may have changed since training), use it instead of answering from memory. Size it to the job: a direct tool for one lookup or action, an agent for multi-step or specialized work. Prior knowledge is a fallback for gaps no capability can fill, never a substitute for one that exists; if you fall back, say so and offer to verify. To answer what tools you have, read your live catalog (including any 'custom tools (load before use)' section), never recite from memory, and never claim you already built a tool without checking.]"

	groundingRule = "[Grounding: state a precise specific (number, name, citation, statute/case/version/ID, date, dosage, direct quote) ONLY when it appears in a tool result or material the user gave you THIS turn, never from memory however confident: a specific you can't point to is worse than none. This holds in casual talk too: give the general shape, but don't attach a specific you can't source, say you're not sure instead of guessing. The [Current date & time: …] stamp on the latest user message IS a this-turn source: read the date and time off it and state them plainly (no tool call needed), but do NOT add a holiday, season, or event association unless a source gave it this turn (a rule-based holiday like \"the last Monday in May\" is exactly the specific you must not assert from memory; \"it's a regular Sunday, what's up?\" beats a confident wrong holiday). If a tool you relied on fails, errors, times out, or returns empty, treat the data as missing, never backfill from memory. Same for MEDIA you couldn't open, download, or view: don't describe it or infer its content from the URL, caption, sender, or nearby messages, and don't reuse one item's description for another; if a past turn's \"[N image(s) attached …]\" note records what an image showed, rely only on that note, don't re-describe or invent its subject. If the user corrects a specific, don't swap in another guess or invent a reason for the error, admit you're unsure and offer to look it up.]"

	actionsRule = "[Actions: never claim you DID something (sent a message/meme/image/file, posted, scheduled, created, saved, ran a command) unless you called the tool that does it THIS turn and its result confirms success. Your reply text is NOT an action: writing 'I sent it', 'attached', 'posted to the group', or 'done' does nothing by itself. If the thing needs a tool, call it and report what its result says; if you didn't call it, don't say you did. If you couldn't or chose not to, say so plainly. When an action tool errors, times out, or returns empty, treat the action as NOT done and tell the user.]"

	disagreeingRule = "[Disagreeing with the user: your training is not enough to tell the user they are wrong. When they state a fact you think is mistaken, treat your priors as possibly stale. If a tool can check it, verify FIRST, then correct with the source in hand. If nothing can verify it, do NOT assert they are wrong from memory: say you are not certain and offer to check, or ask. This is about EMPIRICAL claims (dates, numbers, who/what/when, current state, how something works). Reasoning, math you can show step by step, and the user's own preferences you can still engage directly and decisively.]"

	numbersRule = "[Numbers: reproduce a figure exactly as the source writes it, keep its unit or currency attached, and keep it bound to the thing it describes (which item, date, place) so you never swap two values from the same source. Do not do multi-step arithmetic, percentages, or unit/currency conversion in your head and present the result as fact: show the steps so it can be checked, or use a tool. If two sources disagree, say so rather than silently picking one. (Prices and other time-sensitive figures are governed by [Volatile facts].)]"

	noFalsePrecisionRule = "[No false precision: do NOT manufacture a number for emphasis or authority. Without a real sourced figure, don't invent a percentage, fraction, or dollar amount: say \"most\", \"roughly half\", \"a few thousand\", or describe the size in words. An invented \"80%\" or \"$5,000\" reads as precise and is worse than an honest \"most\". A genuinely sourced number stated exactly is right, as is arithmetic you actually did on sourced numbers (show it), and hedged estimates (\"about half\") are fine.]"

	volatileFactsRule = "[Volatile facts: some facts change over time, and you do NOT know their current value no matter how confident it feels. PRICES are the clearest case: any price, rate, fee, cost, or money figure is volatile, so NEVER state one from memory, not even a rough number or a range; a remembered price is always a guess. The same rule covers stock and availability, the CURRENT holder of a changing role or record (who runs a company now, the latest version of something, the current champion or office-holder), and live status, scores, or counts. The test for any specific: could this have changed since your training, and does the user expect today's value? If yes, it is volatile: {lookup}. Do not fill the gap with a plausible-sounding value. This is not a closed list: any fact that fails the test is volatile even if it is not named here.]"

	secretsRule = "[Secrets: never ask the user to paste an API key, secret, token, or password into the conversation, and do not accept one if offered. Authenticated APIs are wired through gohort credentials set up in Admin > APIs; auth is injected server-side, so you never see or need the secret. If a tool's credential is not configured yet, tell the user it needs to be set up in Admin > APIs (name the credential) and stop there, do not collect login details in chat. A secret typed into a chat leaks into the session history and the tool-call logs, which is what the credential system exists to prevent.]"

	internalMarkersRule = "[Internal markers: anything wrapped in <gohort-meta>...</gohort-meta> is stripped before the user sees it, so use it for internal-only notes and NEVER put anything the user should read inside it. Do not type bare delivery markers like [ATTACH: file] into your reply; attachments ride along through their tool, and any stray marker is scrubbed from your reply anyway.]"

	roundBudgetRule = "[Round budget: this turn has up to {rounds} tool-execution rounds. The framework will nudge you at the halfway mark and again near the cap; plan your investigation so you finish with a real answer rather than hitting the limit mid-exploration.]"
)

// The two branches of the volatile-facts lookup instruction. Which one lands in
// {lookup} depends on the agent's catalogue: naming web_search to an agent that
// has no web tool tells it to call something another layer stripped.
const (
	volatileLookupWeb = "call web_search or fetch_url FIRST and quote what the result returns (with what it applies to and when observed), or, if you cannot look it up right now, say plainly you don't have a current figure and offer to check"

	volatileLookupAny = "look it up FIRST with whatever search or fetch tool you have and quote what the result returns (with what it applies to and when observed); if you have no way to look it up right now, say plainly you don't have a current figure and offer to check"
)

func init() {
	RegisterPromptBlock(PromptBlock{
		Key:      AnsweringRoundsKey,
		Title:    "Answering across tool rounds",
		Category: "Turn discipline",
		Gate:     "Every agent that has tools.",
		Text:     answeringRoundsRule,
	})
	RegisterPromptBlock(PromptBlock{
		Key:      GroundingContractKey,
		Title:    "Grounding contract (framing)",
		Category: "Grounding",
		Gate:     "Every agent that has tools. Frames the grounding blocks below as one rule.",
		Text:     groundingContractRule,
	})
	RegisterPromptBlock(PromptBlock{
		Key:      CapabilityFirstKey,
		Title:    "Capability-first",
		Category: "Grounding",
		Gate:     "Every agent that has tools.",
		Text:     capabilityFirstRule,
	})
	RegisterPromptBlock(PromptBlock{
		Key:      GroundingKey,
		Title:    "Grounding",
		Category: "Grounding",
		Gate:     "Every agent that has tools.",
		Text:     groundingRule,
	})
	RegisterPromptBlock(PromptBlock{
		Key:      ActionsKey,
		Title:    "Actions",
		Category: "Grounding",
		Gate:     "Every agent that has tools.",
		Text:     actionsRule,
	})
	RegisterPromptBlock(PromptBlock{
		Key:      DisagreeingKey,
		Title:    "Disagreeing with the user",
		Category: "Grounding",
		Gate:     "Every agent that has tools.",
		Text:     disagreeingRule,
	})
	RegisterPromptBlock(PromptBlock{
		Key:      NumbersKey,
		Title:    "Numbers",
		Category: "Grounding",
		Gate:     "Every agent that has tools.",
		Text:     numbersRule,
	})
	RegisterPromptBlock(PromptBlock{
		Key:      NoFalsePrecisionKey,
		Title:    "No false precision",
		Category: "Grounding",
		Gate:     "Every agent that has tools.",
		Text:     noFalsePrecisionRule,
	})
	RegisterPromptBlock(PromptBlock{
		Key:      VolatileFactsKey,
		Title:    "Volatile facts",
		Category: "Grounding",
		Gate:     "Every agent that has tools. {lookup} expands to a web_search / fetch_url instruction when the catalogue has one of those tools, and to a use-whatever-you-have instruction otherwise.",
		Text:     volatileFactsRule,
	})
	RegisterPromptBlock(PromptBlock{
		Key:      SecretsKey,
		Title:    "Secrets",
		Category: "Safety",
		Gate:     "Every agent, with or without tools.",
		Text:     secretsRule,
	})
	RegisterPromptBlock(PromptBlock{
		Key:      InternalMarkersKey,
		Title:    "Internal markers",
		Category: "Output",
		Gate:     "Every agent, with or without tools.",
		Text:     internalMarkersRule,
	})
	RegisterPromptBlock(PromptBlock{
		Key:      RoundBudgetKey,
		Title:    "Round budget",
		Category: "Turn discipline",
		Gate:     "Agents whose turn has a budget of 10 or more tool rounds. {rounds} is that budget.",
		Text:     roundBudgetRule,
	})
}

// AnsweringRoundsClause holds the full answer until the final tool-free step,
// so a turn never delivers two complete replies with a tool run between them.
func AnsweringRoundsClause() string {
	return EffectiveBlockText(AnsweringRoundsKey, answeringRoundsRule)
}

// GroundingContractClause frames the blocks that follow as one rule seen from
// several sides. Append it before them or not at all.
func GroundingContractClause() string {
	return EffectiveBlockText(GroundingContractKey, groundingContractRule)
}

// CapabilityFirstClause governs tool SELECTION: reach for a capability that can
// do the job rather than answering a changeable question from training.
func CapabilityFirstClause() string {
	return EffectiveBlockText(CapabilityFirstKey, capabilityFirstRule)
}

// GroundingClause governs specifics once results are in hand: a number, name,
// citation or quote is earned by pointing at where it came from this turn.
func GroundingClause() string { return EffectiveBlockText(GroundingKey, groundingRule) }

// ActionsClause is Grounding aimed at actions rather than facts: reply text is
// not an action, so "I sent it" without the tool call is a false claim.
func ActionsClause() string { return EffectiveBlockText(ActionsKey, actionsRule) }

// DisagreeingClause points the same rule the other way: do not tell the user
// they are wrong from memory alone.
func DisagreeingClause() string { return EffectiveBlockText(DisagreeingKey, disagreeingRule) }

// NumbersClause keeps a figure bound to its unit and its subject, and keeps
// unshown arithmetic out of the reply.
func NumbersClause() string { return EffectiveBlockText(NumbersKey, numbersRule) }

// NoFalsePrecisionClause forbids inventing a number for emphasis. "Most" beats
// a manufactured "80%".
func NoFalsePrecisionClause() string {
	return EffectiveBlockText(NoFalsePrecisionKey, noFalsePrecisionRule)
}

// SecretsClause stops an agent soliciting credentials in chat, where they would
// land in session history and tool-call logs.
func SecretsClause() string { return EffectiveBlockText(SecretsKey, secretsRule) }

// InternalMarkersClause gives the model a sanctioned scrubbed wrapper and tells
// it not to type bare delivery markers into user-facing text.
func InternalMarkersClause() string {
	return EffectiveBlockText(InternalMarkersKey, internalMarkersRule)
}

// VolatileFactsClause names the lookup route the agent actually has. hasWebTool
// reports whether its catalogue carries web_search, fetch_url or browse_page.
func VolatileFactsClause(hasWebTool bool) string {
	lookup := volatileLookupAny
	if hasWebTool {
		lookup = volatileLookupWeb
	}
	return expandClause(VolatileFactsKey, volatileFactsRule, "{lookup}", lookup)
}

// RoundBudgetClause tells the turn how many tool rounds it has, so the model
// paces its investigation instead of being truncated mid-exploration.
func RoundBudgetClause(maxRounds int) string {
	return expandClause(RoundBudgetKey, roundBudgetRule, "{rounds}", strconv.Itoa(maxRounds))
}

// expandClause fills a templated block's placeholder, and falls back to the
// shipped wording when an edit has dropped it.
//
// The fallback is not pedantry. An operator edit reaches these through the same
// text override as any other block, and the Prompts page also offers an
// LLM rewrite pass over every block at once — a rewrite that "tidies away"
// {rounds} leaves the model told it has "up to tool-execution rounds", and one
// that drops {lookup} leaves the volatile-facts rule naming no way to check.
// Both read as fluent prose, so nothing downstream would notice. Refusing the
// broken text costs the operator their edit on that one block and keeps the
// rule intact, which is the right way round for a clause that exists to stop
// fabrication.
//
// Breadcrumbed once per key: a silent fallback would leave a person staring at
// their edit on the page wondering why nothing changed.
func expandClause(key, def, placeholder, value string) string {
	text := EffectiveBlockText(key, def)
	if text == "" {
		return "" // switched off
	}
	if !strings.Contains(text, placeholder) {
		warnOnce(key, "[prompts] %s: the edited text has no %s, so the shipped wording is used instead", key, placeholder)
		text = def
	}
	return strings.ReplaceAll(text, placeholder, value)
}

var (
	warnedMu sync.Mutex
	warned   = map[string]bool{}
)

func warnOnce(key, format string, args ...interface{}) {
	warnedMu.Lock()
	seen := warned[key]
	warned[key] = true
	warnedMu.Unlock()
	if !seen {
		nfo.Log(fmt.Sprintf(format, args...))
	}
}
