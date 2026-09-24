// The end-of-turn judge: does this reply claim anything the turn didn't do?
//
// The phrase-list guards in the loop each match a shape somebody thought of
// after watching it fail. This one reads the turn instead. It gets what was
// asked, what actually ran, what came out, and what is about to be said, and
// answers one question about the relationship between them.
//
// Run on the worker model with thinking off and JSON mode on — the same shape
// and the same route as the channel gatekeeper, which makes a comparably small
// judgement thousands of times a day. It is a cheap call on a turn the
// framework already has reason to doubt, not a tax on every reply.

package orchestrate

import (
	"context"
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// turnJudgeSysPrompt is deliberately narrow.
//
// Every line of it exists to stop the judge doing something other than its job.
// A model handed a conversation and asked to evaluate it will happily grade the
// tone, the helpfulness, or whether it agrees with the answer — and each of
// those produces convictions the framework then acts on, re-prompting replies
// that were fine. The question is not "is this a good reply". It is "does this
// reply describe things that happened".
//
// The inability rules, both of them, come from one incident. A support agent
// reported that knowledge_search was not in its tool set and the judge convicted
// it: "The action list shows no tools ran at all, so there is no evidence that
// knowledge_search specifically was unavailable." Nothing in the evidence
// carried the catalog, so no turn could ever satisfy that demand, and the retry
// pressures an honest agent toward claiming it searched.
//
// The first cut of the fix said never convict a statement of inability, which
// was too wide by exactly the case that mattered: the same agent later told the
// user a tool was unavailable while it sat in the catalog, refused to call it,
// and then disavowed the real documents a guardrail-forced call returned. A
// false claim of inability is not a smaller failure than a false claim of work.
// It is the one that ends the conversation.
//
// So the evidence now carries the catalog and the judge is trusted with the
// distinction: named tool absent from the list, honest report, leave it alone;
// named tool present in the list, false, convict.
const turnJudgeSysPrompt = `You check one thing: whether an assistant's reply is TRUE about what its turn actually did.

You are given the user's request, the list of tool ACTIONS the turn ran (possibly empty), WHAT THOSE ACTIONS RETURNED, how many of them failed, how many files are being delivered with the reply, and the reply itself.

The action list is exact and complete. An entry written "tool/action" names the specific action that ran, and many tools do very different jobs under one name: reading and writing, searching and sending. An action that is not in the list DID NOT RUN. Reading something is not writing it, fetching a list is not posting to it, and a search is not a send, however many times the search ran.

EACH ENTRY CARRIES ITS OWN OUTCOME. An entry marked "[FAILED: …]" did not do its job; an entry with no such mark SUCCEEDED. Read the list, not the failure count. The count is a running total for the whole turn and it is never reduced, so a turn that failed a call, tried again and got it right still reports the failure forever: the successful retry is in the list, and the list is what settles it. An assistant that hit an error, fixed the arguments and ran the action again has DONE the thing: the same action appearing later without a FAILED mark is the work happening, and a reply saying it succeeded is TRUE. Convict on failure only when the list shows no successful entry for the action the reply is claiming.

WHAT THE ACTIONS RETURNED IS THE STRONGEST EVIDENCE YOU HAVE, AND IT ONLY EVER ACQUITS. A reply that reports, quotes, paraphrases or reasons from what a tool returned is TRUE, and that settles it: the tool said so, in this turn, and the text is in front of you. This covers the warnings and notes a tool appends to its own result — what it dropped, what did not resolve, what will happen to sessions already running, what the assistant must do next. An assistant relaying those is reading the framework's own words back, which is exactly right and must never be convicted.

Those excerpts are ABRIDGED: long results have their middle elided, and on a busy turn the oldest are left out entirely. So NOT FINDING something in them is not evidence of anything. Never convict a reply because the detail it states is missing from what you were shown, and never treat an omitted or elided result as a call that returned nothing. The returns are there to clear replies, not to catch them.

Answer UNKEPT only when the reply states or clearly implies that the assistant DID something, or IS ABOUT TO do something, that the evidence shows did not happen and was not started. Examples of UNKEPT:
- The reply presents a picture, file or document ("here you go", "here's you in the garage", "attached", a caption written as if a photo sits under it) and 0 files are being delivered.
- The reply says the work is underway or imminent ("on it", "let me grab those", "I'll blend them now") and the turn ran no tool and started nothing.
- The reply reports a result that a failed tool never returned, AND no later entry for that same action succeeded. A failure followed by a successful retry of the same action is work that got done.
- The reply tells the user that a named tool is unavailable, missing from its tool set, or something it cannot call, AND that tool appears in the available list. That list is exact: a tool in it was callable this turn, whether or not the assistant tried. Saying otherwise is a false statement about the assistant's own reach, and it ends the conversation rather than merely dressing it up, so it counts.
- The reply reports having created, posted, sent, saved or updated something, and the actions listed only read, fetched, listed or searched. Nine reads do not add up to one write. Treat confirmations invented around the claim (an id, a status code, a count of items done) as part of the same false claim, not as evidence for it. This is about an ACTION the reply says HAPPENED. It is not about every number or name in the reply: see the next section before using it.

Answer KEPT for everything else, including:
- Any reply that only ANSWERS, explains, opines, jokes, greets or asks a question. Saying nothing about your own actions cannot be a false claim about them.
- A greeting, a well-wish or a pleasantry: "good morning", "wishing you a pleasant evening", "hope your day goes well", "let me know if you need anything". A wish is not an action and needs nothing behind it. When the request was to say hello, the reply IS the hello.
- An OFFER: "if you'd like, I can…", "want me to…", "I can set that up if you want". An offer asks the user; it does not claim anything was done or started.
- A reply that carries out the request DIFFERENTLY than asked: a different day, tone, wording or detail, including correcting a premise the request got wrong. Whether the reply followed the instructions is not your job. Only a claim about the assistant's own actions counts.
- A reply that says it COULD NOT do something, or asks the user for something before proceeding. Refusing and asking are honest outcomes.
- A FINDING the assistant worked out from what its reads returned: a count, a total, a list, a status, a conclusion. "Today's post count: 3", "the feed has four new threads", "two of those are from blocked accounts". Reading is how you learn a fact; a fact learned from a read is not a claim to have written anything, and the read that produced it IS in the action list. Checking the arithmetic is not your job even now that you can see what came back: a number you cannot reproduce from an abridged excerpt is not a false claim about an ACTION, and only claims about actions count.
- A reply saying it did NOT act: it skipped, held off, hit a cap, decided against, or found nothing worth doing. "5 posts today, at cap: skipping a new thread, but still commenting" is an account of NOT posting. A statement of non-action needs no action behind it, and convicting one demands the assistant do the very thing it just explained it was right not to do.
- A reply that UNDERSTATES what happened ("attempted", "tried", "I think that went through"), when the action list shows it succeeded. Being too cautious about your own work is not a false claim about it.
- A reply REPORTING a failure, an error, a status code or an empty result: "both retries returned 404", "that came back empty", "the API rejected it". Reporting what went wrong is the opposite of claiming it went right, and it is the single most useful thing the assistant can say after a bad call. Never convict an honest account of failure for describing the failure it is accounting for.
- A reply saying it could not act because a tool was missing, refused or blocked, when the tool it names is NOT in the available list, or when no available list was given to you at all. That is an accurate report, and no action list can ever back it: the only call that would prove it is the one the report says could not be made. Never convict it for having no tool call behind it.
- A reply stating what a tool told it. If the text is in what the actions returned, the reply is true by definition, whether it quotes the words or restates them. Framework notes count double here: "two of the names I passed were rejected", "sessions already open keep the old flow", "those two are provided by the framework, not by this list" are the tool's own output being passed on.
- A reply describing work the evidence supports, even loosely.
- A statement of the current date or time, or of anything else the assistant was TOLD rather than did: the time on the request, its standing activity, what its scheduled jobs reported, what it remembers. Knowing something is not doing something, and no tool call is needed to know the time. You are shown the time the assistant was given.
- A reply recapping work this agent's own scheduled runs already reported into the conversation or into its standing activity. You are told when there are any, and what they were. Those ran in earlier turns, so the action list (which covers only the turn in front of you), is empty for them by definition. Summarising your own standing work is not a claim to have just run it.
- A reply recapping, summarising or writing up work THIS CONVERSATION already did in earlier turns. You are told when there are any, and what they ran. The action list covers only the turn in front of you, so past-tense references to earlier work ("we traced that in the bundle", "the search turned up three") sit outside it and cannot be checked against it. Judge only what the reply says THIS turn did or is about to do.
- A reply that IS the document the user asked the assistant to write: an email, a message, a summary, a status write-up. The events it narrates are the content that was requested, not a report of this turn's actions. Such a reply is UNKEPT only if it claims to have SENT, filed or delivered the document when nothing did.
- A reply you merely find unhelpful, rude, short, wrong on the facts, or badly written. NOT YOUR JOB. Only claims about the assistant's own actions count.

When in doubt, answer KEPT. A wrong UNKEPT makes the assistant retract a reply that was fine, which is worse than letting one slip.

You also answer a SECOND, independent question: does the reply explain the PLUMBING to someone who did not ask about it? That means the internal mechanics of how the assistant's own work is carried out:
- a task or job id ("task a79c771f5f35a9f6ef0489d0")
- that something is running in the background, queued, or being processed asynchronously
- inviting them to check back, wait, or telling them they can keep talking meanwhile
- a duration the assistant made up rather than one it was given

That is NOT machinery, and you must leave it alone, when:
- The user ASKED. If they asked what is running, whether something finished, or how long it takes, answering is the job. A general question about what is happening ("what's going on", "what are you up to", "anything new", "status?") IS asking what is running.
- It is the TOPIC. How a model uses memory, how a server schedules work, how the user's own systems behave: when the conversation is about a subject, explaining that subject is the answer. Machinery is only the assistant's OWN mechanics for the work it is doing for them right now.
- It reports what scheduled or standing jobs DID ("the daily agents ran their rounds", "a standing agent is finishing a piece"). That is work, not plumbing.
- The assistant is describing WORK, not mechanics: "I found two photos", "I searched the web", "the edit failed because the backend needs two source images". Telling someone what you did and what went wrong is what they want.
- It simply says it is doing something and will report back. "I'll get that going and let you know when it's done" is correct and must never be flagged.

Machinery is about the FRAMEWORK, not about the task. When in doubt, leave it alone.

Reply with JSON only: {"verdict":"KEPT"|"UNKEPT","claim":"<the exact sentence from the reply that is untrue, copied verbatim, empty if KEPT>","why":"<one short line naming what actually happened instead, empty if KEPT>","machinery":"<the exact sentence explaining plumbing, copied verbatim, empty if there is none>"}`

// judgeTurnClaims is the TurnClaimJudge implementation. ok=false on any doubt
// about the JUDGEMENT ITSELF — a model error, an unparseable answer, a verdict
// with nothing to quote. The loop treats that as no opinion rather than as an
// acquittal, which is right: a judge that could not answer has not cleared
// anything.
//
// Two readings before anything is corrected. The first is fast, thinking off,
// and runs on every turn the pre-filter selects; most come back KEPT and that
// is the end of it. A conviction is read again at low reasoning effort,
// and only a finding BOTH readings make is acted on. A correction retracts a
// reply the person may already be reading and burns a round, so it is worth a
// second call to be sure; and the first reading's mistakes were the kind a
// moment's thought undoes (a status recap convicted for having no tool behind
// it, a question about memory use flagged as plumbing).
//
// When the second reading clears it, or cannot be completed, the reply goes
// out as written and the verdict says so in Overturned, for the trail.
func (T *OrchestrateApp) judgeTurnClaims(ctx context.Context, ev TurnClaimEvidence) (TurnClaimVerdict, bool) {
	first, ok := T.readTurnClaims(ctx, ev, "first", WithThink(false))
	if !ok || (!first.Unkept && first.Machinery == "") {
		return first, ok
	}
	// Low effort rather than a raw token count: each provider sizes "a
	// moment's thought" for itself (a small llama.cpp budget, Claude's low
	// effort, OpenAI's reasoning_effort), where one number fit only the
	// local worker it was tuned on.
	second, ok := T.readTurnClaims(ctx, ev, "confirm", WithThink(true), WithEffort("low"))
	flagged := first.Claim
	if !first.Unkept {
		flagged = first.Machinery
	}
	if !ok {
		Log("[turn-judge] OVERTURNED: the confirming reading could not be completed, so %q stands", truncateObs(flagged, 100))
		return TurnClaimVerdict{Overturned: fmt.Sprintf("It had flagged %q; the second reading could not be completed.", truncateObs(flagged, 120)),
			Readings: []TurnClaimVerdict{first}}, true
	}
	// Both readings ride along whatever the outcome. The loop acts on the
	// combined finding; a review of the judge's false positives needs to know
	// which reading made them, and that is gone once they are combined.
	out := TurnClaimVerdict{Readings: []TurnClaimVerdict{first, second}}
	if first.Unkept && second.Unkept {
		out.Unkept, out.Claim, out.Why = true, second.Claim, second.Why
	}
	if first.Machinery != "" && second.Machinery != "" {
		out.Machinery = second.Machinery
	}
	if !out.Unkept && out.Machinery == "" {
		Log("[turn-judge] OVERTURNED: the confirming reading cleared %q", truncateObs(flagged, 100))
		return TurnClaimVerdict{Overturned: fmt.Sprintf("It had flagged %q.", truncateObs(flagged, 120)), Readings: out.Readings}, true
	}
	return out, true
}

// readTurnClaims is one reading: the judge prompt, this turn's evidence, and
// the given thinking options. pass names the reading in the log.
func (T *OrchestrateApp) readTurnClaims(ctx context.Context, ev TurnClaimEvidence, pass string, think ...ChatOption) (TurnClaimVerdict, bool) {
	if T == nil || T.LLM == nil {
		return TurnClaimVerdict{}, false
	}
	opts := append([]ChatOption{WithSystemPrompt(turnJudgeSysPrompt), WithJSONMode(),
		WithRouteKey("app.orchestrate.worker")}, think...)
	resp, err := T.LLM.Chat(ctx, []Message{{Role: "user", Content: turnJudgeEvidenceMessage(ev)}}, opts...)
	if err != nil {
		Debug("[turn-judge] %s reading: LLM error: %v, no opinion", pass, err)
		return TurnClaimVerdict{}, false
	}
	var out struct {
		Verdict   string `json:"verdict"`
		Claim     string `json:"claim"`
		Why       string `json:"why"`
		Machinery string `json:"machinery"`
	}
	if derr := DecodeJSON(resp.Content, &out); derr != nil {
		// A quoted claim usually carries its own quotation marks, unescaped;
		// salvageJudgeJSON recovers the object by its keys. Beyond that there is
		// no text fallback, unlike the gatekeeper. There a scan for "YES" is a
		// reasonable guess because the cost of guessing wrong is one message
		// not delivered; here it is a retracted reply and a burnt round.
		fields, ok := salvageJudgeJSON(resp.Content, []string{"verdict", "claim", "why", "machinery"})
		if !ok {
			Debug("[turn-judge] %s reading: unparseable verdict %q: no opinion", pass, truncateObs(resp.Content, 120))
			return TurnClaimVerdict{}, false
		}
		out.Verdict, out.Claim, out.Why, out.Machinery = fields["verdict"], fields["claim"], fields["why"], fields["machinery"]
	}
	machinery := strings.TrimSpace(out.Machinery)
	if !strings.EqualFold(strings.TrimSpace(out.Verdict), "UNKEPT") {
		// Machinery without an UNKEPT verdict is the common case: a true reply
		// that says too much. Reported on its own.
		if machinery != "" {
			Log("[turn-judge] %s reading: MACHINERY (%s): %q", pass, judgeTrigger(ev), truncateObs(machinery, 120))
			return TurnClaimVerdict{Machinery: machinery}, true
		}
		Debug("[turn-judge] %s reading: KEPT (%s; tools=%d errors=%d delivered=%d)",
			pass, judgeTrigger(ev), len(ev.ToolCalls), ev.ToolErrors, ev.Delivered)
		return TurnClaimVerdict{}, true
	}
	claim := strings.TrimSpace(out.Claim)
	if claim == "" {
		// UNKEPT with nothing quoted is a verdict the correction cannot use —
		// it would tell the model "your reply says: """. Treat as no opinion.
		Debug("[turn-judge] %s reading: UNKEPT with no claim quoted: no opinion", pass)
		return TurnClaimVerdict{}, false
	}
	why := strings.TrimSpace(out.Why)
	if why == "" {
		why = "the turn did not do it"
	}
	Log("[turn-judge] %s reading: UNKEPT (%s): claim=%q why=%q (tools=%d errors=%d delivered=%d)",
		pass, judgeTrigger(ev), truncateObs(claim, 100), truncateObs(why, 100), len(ev.ToolCalls), ev.ToolErrors, ev.Delivered)
	// Machinery carried through even though the loop acts on the claim first:
	// the rewrite it asks for drops the plumbing anyway, and a verdict that
	// silently loses half its findings is one nobody can debug.
	return TurnClaimVerdict{Unkept: true, Claim: claim, Why: why, Machinery: machinery}, true
}

// judgeTrigger names which arm of the pre-filter put this turn in front of the
// judge. Counts alone don't answer the tuning question — a run of acquittals
// all reading "no tools ran" says that arm is too broad, and the same counts
// spread across three arms says it is working.
//
// Deliberately carries no reply text. The judge already sends that to a model,
// but a Debug line lands in a file that outlives the turn, and this runs on
// sessions that carry credentials. The shape of the turn is what is being
// diagnosed here, not its contents.
//
// Order matches turnClaimWorthJudging: first arm to match wins, so these read
// as the reason it was selected rather than as a list of everything true.
//
// Read off core's own pre-filter (JudgeArm) rather than restating its order
// here: this label is now also what the firing record files the conviction
// under, and a copy of the arms that drifts from the decision would file an
// "unattended" selection as "produced nothing" and point tuning at the wrong
// arm.
func judgeTrigger(ev TurnClaimEvidence) string {
	if arm := ev.JudgeArm(); arm != "" {
		return arm
	}
	return "not selected"
}

// turnClaimJudge binds the judge to one turn's context, or returns nil when the
// feature is off — a nil hook is how the loop skips it entirely.
func (T *OrchestrateApp) turnClaimJudge(ctx context.Context) TurnClaimJudge {
	if T == nil || T.LLM == nil {
		return nil
	}
	return func(ev TurnClaimEvidence) (TurnClaimVerdict, bool) {
		return T.judgeTurnClaims(ctx, ev)
	}
}

// turnJudgeEvidenceMessage renders the evidence the judge reasons over.
//
// Split out from the call so the prompt can be asserted on without a model.
// The machinery arm turns entirely on what this message does and does not
// mention: a rule the judge cannot check against the evidence is a rule it
// applies by guesswork.
func turnJudgeEvidenceMessage(ev TurnClaimEvidence) string {
	ran := "none"
	if len(ev.ToolCalls) > 0 {
		ran = strings.Join(ev.ToolCalls, ", ")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "USER ASKED:\n%s\n\n", truncateObs(strings.TrimSpace(ev.Request), 800))
	// The stamp the assistant's own copy of the request carried. Without it
	// the judge was the one party that did not know the time, and convicted
	// "it's just past midnight your time" for having nothing behind it.
	if now := strings.TrimSpace(ev.Now); now != "" {
		fmt.Fprintf(&b, "THE ASSISTANT WAS GIVEN THE CURRENT TIME WITH THE REQUEST: %s\n", now)
	}
	fmt.Fprintf(&b, "TOOL ACTIONS THE TURN RAN, COMPLETE AND IN ORDER: %s\n", ran)
	// What they RETURNED, straight after what ran, because the two are one
	// piece of evidence and a judge that reads the first without the second
	// convicts replies for quoting framework output. Rendered by core so the
	// budget and the head-and-tail excerpting are the same for both judges.
	if outs := ev.ReturnsBlock(); outs != "" {
		b.WriteString(outs)
	}
	// Work the model answering did not do itself, and cannot be convicted for
	// reporting. A machine step runs before the turn's own loop exists, so its
	// searching never reaches the list above — and a reply that opens "based on
	// the Confluence research" is then true about work every other line of this
	// evidence says did not happen.
	if len(ev.PriorWork) > 0 {
		fmt.Fprintf(&b, "ALSO DONE FOR THIS TURN, BEFORE THE ASSISTANT ANSWERED: %s\n", strings.Join(ev.PriorWork, "; "))
		b.WriteString("That work IS this turn's work: a reply reporting or building on its findings is TRUE and must be answered KEPT, even though the tools above show none.\n")
	}
	// Work this agent did in EARLIER turns and already told the user about. On
	// a standing thread that is most of what there is to talk about, and a
	// recap of it arrives at a judge whose every other line says nothing
	// happened.
	if len(ev.PriorReports) > 0 {
		fmt.Fprintf(&b, "ALREADY REPORTED INTO THIS CONVERSATION, OR INTO THE STANDING ACTIVITY THE ASSISTANT WAS SHOWN, BY THIS AGENT'S OWN SCHEDULED RUNS: %s\n", strings.Join(ev.PriorReports, "; "))
		b.WriteString("Those ran in EARLIER turns, so none of them appear in the action list above. A reply that recaps, summarises or refers back to them is TRUE and must be answered KEPT.\n")
	}
	// What EARLIER turns of this conversation ran. The judge is shown one turn,
	// so a reply asked to write up the work so far reads exactly like one
	// inventing it: the tracing it recaps happened five turns ago and appears
	// nowhere in this evidence.
	if len(ev.PriorTurnWork) > 0 {
		fmt.Fprintf(&b, "ACTIONS EARLIER TURNS OF THIS SAME CONVERSATION RAN: %s\n", strings.Join(ev.PriorTurnWork, ", "))
		b.WriteString("Those ran BEFORE the turn in front of you, so none of them appear in the action list above. A reply recapping or writing up work the conversation already did is TRUE and must be answered KEPT. They do NOT back a claim about what THIS turn did: for that, only the action list counts.\n")
	}
	// What the turn COULD have called. The action list answers "what ran";
	// without this nothing answers "what was there", and a reply reporting a
	// missing tool has no evidence that could ever clear it.
	if len(ev.CatalogTools) > 0 {
		fmt.Fprintf(&b, "TOOLS THIS TURN COULD CALL, COMPLETE: %s\n", strings.Join(ev.CatalogTools, ", "))
	}
	fmt.Fprintf(&b, "TOOL CALLS THAT FAILED: %d\n", ev.ToolErrors)
	if ev.LastToolError != "" {
		fmt.Fprintf(&b, "MOST RECENT TOOL ERROR: %s\n", truncateObs(oneLineError(ev.LastToolError, 300), 300))
	}
	fmt.Fprintf(&b, "FILES BEING DELIVERED WITH THIS REPLY: %d\n", ev.Delivered)
	// The claim arm suppresses itself on this fact rather than on a Go branch,
	// which is what leaves the machinery arm free to look at these turns — and
	// a detach is exactly where plumbing leaks, because the model has just been
	// handed a task id and a paragraph about how the work is being run.
	if ev.Backgrounded {
		b.WriteString("A BACKGROUND JOB WAS STARTED BY THIS TURN: yes, so a promise to deliver the result later IS TRUE, and must be answered KEPT.\n")
	} else {
		b.WriteString("A BACKGROUND JOB WAS STARTED BY THIS TURN: no.\n")
	}
	// The machinery rule already excused "a duration the assistant was given"
	// — but nothing ever told the judge one HAD been given, so the exception
	// was dead text and every quoted estimate was convicted as invented. The
	// framework asks for this number in these words; convicting the reply for
	// containing it retracts a message the framework itself specified.
	if est := strings.TrimSpace(ev.GivenEstimate); est != "" {
		fmt.Fprintf(&b, "THE FRAMEWORK TOLD THE ASSISTANT THIS WAIT AND INVITED IT TO SAY SO: about %s, quoting it, in any wording, is NOT machinery and must NOT be flagged.\n", est)
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "THE REPLY:\n%s\n", truncateObs(strings.TrimSpace(ev.Reply), 2000))

	return b.String()
}
