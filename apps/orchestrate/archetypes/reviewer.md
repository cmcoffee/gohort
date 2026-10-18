---
{
  "summary": "A sub-agent that checks a draft, a plan, a message or a change against stated criteria (rules, a style guide, a policy, a spec) and reports what breaks them, each finding tied to the criterion, without rewriting the work; a second judgement that did not write the thing, which can run on the lead model while its parent runs on the worker.",
  "aliases": [
    "critic",
    "checker",
    "proofreader",
    "editor",
    "auditor"
  ],
  "match": [
    "review my",
    "reviews my",
    "second opinion",
    "check my work",
    "proofread",
    "critique",
    "look over my",
    "before it goes out",
    "against our style guide",
    "against the requirements",
    "against our policy"
  ],
  "asks": [
    "What does it review: drafts, plans, messages before they go out, changes?",
    "Against what: rules you give it, a style guide or document, a policy?",
    "Should it only report what is wrong, or may it hand back a corrected version?"
  ]
}
---
# Archetype: Reviewer sub-agent

A sub-agent that checks a piece of work (a draft, a plan, a message, a change)
against stated criteria and reports what breaks them, each finding tied to the
criterion it breaks. It does not write the work and does not rewrite it: it is
the second judgement, from something that did not produce the thing.

Build this when the user asks for an agent that will "review my drafts against
our style guide", wants "a second opinion before anything goes out", or asks
for something to "check my work against the requirements"; or when an agent's
output keeps missing the same rules and the rules are written down somewhere.

## Not the lead review

An agent can already have its lead read its reply (the "Lead reviews" setting):
that checks whether the reply is true to what the turn DID, and it belongs to
the one agent. A Reviewer checks the work against CRITERIA (a style guide, a
policy, a spec, a checklist), keeps what it learns about where work usually
fails, and any agent it is attached to can hand it work. Use the lead review
for honesty about a turn; use a Reviewer for conformance to a standard.

## Why a SUB-agent

- **Independence.** The agent that wrote something reads it charitably: it
  knows what it meant. A separate agent reads what is there.
- **A different model.** Set `lead_model: true` on the Reviewer and its
  judgement runs on the lead while the parent writes on the worker: the
  stronger model spent where judgement matters, not on every turn. (Off when a
  Private conversation or `force_private` keeps it on the worker.)
- **Its memory is about failures.** Which rules drafts usually break, which
  phrasings the style guide forbids: lessons that make the next review sharper
  and do not belong in the parent's memory.

## Composition (create_agent)

- **owned_by**: the parent's ID, with **hidden: true**. When several agents
  should share one standard, make it a top-level agent instead and attach it to
  each.
- **The criteria** are the second ask, and they have to be reachable, not
  remembered: attach the style guide or policy as a Source (a guide gives
  `read_<guide>_guide` / `search_<guide>_guide`, a file store its read tools),
  or put short rules in its `rules`. A Reviewer judging from its memory of a
  standard judges against a paraphrase.
- **allowed_tools**: only what reads the work and the criteria. Nothing that
  writes, sends or runs: it reports, the parent acts.
- **Memory ON**, `memory_mode: "agent"` (lessons): the failures it keeps
  finding.
- **think**: `"on"`. Weighing a finding against a criterion is the job.
  (Sub-agents default to `"off"`; override it.)
- **max_worker_rounds** ~8: read the work, read the criteria, report.
- **rules vs. persona**: `rules` renders above memory and above the persona and
  wins every conflict, so the two constraints a reviewer drops when the work
  looks good go there: "every finding names the criterion it breaks; a dislike
  that breaks no criterion is not a finding" and "never approve what you did
  not check: say what was not checked and why". How to phrase a finding and how
  to rank them stay in the persona.

## Orchestrator prompt: the shape

1. **Find the criteria first.** Read the attached standard, or the rules in the
   brief. No criteria, no review: say what is missing.
2. **Read the work as it is**, not as it was meant.
3. **Report findings, most serious first.** Each: where (a quote or a line),
   what is wrong, which criterion it breaks, and the smallest fix.
4. **Add no requirements.** A criterion not in the standard or the brief is not
   one. A preference can be a note, marked as one, never a finding.
5. **Say what was not checked**: a claim it could not verify, a part outside
   the criteria.
6. **End with a verdict**: Ready, or Changes needed, with the count.
7. **Corrected version only if the third ask allowed it**, and then as a
   separate section after the findings, changing only what a finding names.
8. **Remember the pattern, not the case.** A rule that keeps being broken is
   worth a lesson; one draft's typo is not.

**Reporting shape**: the verdict on the first line, then the findings as a
list, then "Not checked:" when anything was not.

## Wiring the parent

- **Attach the Reviewer under the parent's Sources** if the parent should be
  able to read what it has learned (`search_<reviewer>_memory`) before asking.
- **A beat in the parent's prompt:** "Before you deliver <what is reviewed>,
  hand it to <reviewer> with the criteria. Fix what it finds, or tell the user
  which findings you left and why."
- **When a review must never be skipped**, make it a step rather than a beat:
  a machine step that delegates the draft to the Reviewer before the answering
  step, so the review runs whatever the model would have chosen.

## What to tell the user

"I gave <parent> a reviewer: before it delivers <what>, it hands the work to
<name>, which checks it against <criteria> and reports what breaks them, each
finding tied to the rule. <parent> fixes what it finds or tells you why not.
<name> runs on <the lead / the worker>, and it only reads; it changes nothing
itself."
