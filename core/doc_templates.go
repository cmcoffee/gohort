// Shared document skeletons offered by the writer apps (CodeWriter,
// TechWriter) when starting a new markdown document.

package core

import "github.com/cmcoffee/gohort/core/ui"

// MarkdownDocTemplates are the starting skeletons a writer app offers for
// a new markdown document. They exist to kill the blank page for the
// handful of documents people write repeatedly and never remember the
// shape of.
//
// Shared rather than per-app: a runbook is a runbook whether it was
// started in CodeWriter or TechWriter, and an app that had to declare
// its own set would either duplicate these or drift from them.
//
// Each Body is a real outline. It lands in the sections editor as
// navigable blocks, so headings the author fills in beat paragraphs
// telling them what to write. Keep the prompts inside each section to a
// single italic line — it reads as a hint and deletes in one keystroke.
var MarkdownDocTemplates = []ui.DocTemplate{
	{
		Name:        "Agent prompt",
		Description: "System prompt for an agent: role, approach, rules, failure modes, output.",
		// Headings mirror the agent editor's declared outline
		// (orchestratorPromptSections in apps/orchestrate), so a prompt
		// drafted here drops into that editor as filled sections rather
		// than one unsectioned blob. codewriter can't import orchestrate,
		// so the correspondence is by convention — if that outline
		// changes, change these too.
		Body: `# <agent name>

## Role & voice
*Who this agent is, what it's accountable for, and how it sounds. Two or three sentences, written to the agent: "You are…"*

## Approach
*How it works a request: what it does first, how it decomposes, when it asks instead of assuming.*

## Rules
- always <hard constraint the agent can check itself against>
- never <the thing that must not happen>

## Failure modes
- ambiguous request — <what to do>
- no results — <what to do, instead of guessing>
- sources disagree — <what to do>

## Output format
*Length, structure, whether to cite, when to use code blocks or tables.*
`,
	},
	{
		Name:        "Skill",
		Description: "A skill pack: when it activates and the guidance it adds.",
		Body: `# <skill name>

## Use when
*The activation cue, written as if completing "Use when the user…". This is what the host model reads to decide whether to consult the skill, so name situations, not topics.*

## Triggers
*Optional precision nudge: disambiguating phrases, not standalone words.*

- <phrase that only appears in this domain>

## Guidance
*Additive instructions: "when this kind of task comes up, also do X." The framework prepends the skill's own header, so start at the content.*

## Procedure
1. first step
2. second step

## Watch out for
*The mistakes this domain invites, and what to do instead.*
`,
	},
	{
		Name:        "Runbook",
		Description: "Operational procedure: when to run it, the steps, how to tell it worked.",
		Body: `# Runbook: <name>

## When to use this
*The symptom or request that sends someone here.*

## Before you start
- access or credentials needed
- who to tell

## Steps
1. first action
2. second action

## Verify
*How you know it worked. A command and its expected output.*

## If it goes wrong
*The most likely failure and what to do about it.*

## Rollback
*How to undo the change.*
`,
	},
	{
		Name:        "Postmortem",
		Description: "Incident writeup: impact, timeline, causes, follow-ups.",
		Body: `# Postmortem: <incident>

## Summary
*What broke, for whom, for how long. Two or three sentences.*

## Impact
- users affected:
- duration:
- data loss:

## Timeline
*Times in one zone, stated. What was observed, not what was concluded later.*

- HH:MM — first signal
- HH:MM — detected
- HH:MM — mitigated
- HH:MM — resolved

## What happened
*The mechanism. How the failure actually propagated.*

## Why it wasn't caught sooner
*Detection gap, not blame.*

## What went well

## Follow-up actions
- [ ] action — owner — date
`,
	},
	{
		Name:        "Decision record",
		Description: "One decision: the context, the options weighed, what was chosen and why.",
		Body: `# <decision>

**Status:** proposed
**Date:**

## Context
*The forces in play. What makes this a decision rather than an obvious call.*

## Options considered

### Option A
*Trade-offs.*

### Option B
*Trade-offs.*

## Decision
*What was chosen, in one sentence, then why it beat the alternatives.*

## Consequences
*What this commits us to, including the parts we won't like.*
`,
	},
	{
		Name:        "How-to guide",
		Description: "Task-focused walkthrough: goal, prerequisites, steps, verification.",
		Body: `# How to <task>

## Goal
*What you'll have when you're done.*

## Before you start
- prerequisite
- prerequisite

## Steps
1. first step
2. second step

## Check it worked
*The observable result.*

## Troubleshooting
*Common failure and its fix.*
`,
	},
	{
		Name:        "README",
		Description: "Project front page: what it is, how to run it, how to work on it.",
		Body: `# <project>

*One sentence: what this is and who it's for.*

## Install

## Usage

## Configuration
| Setting | Default | What it does |
| --- | --- | --- |

## Development
*How to build, test, and run it locally.*

## Notes
*Anything surprising a newcomer would otherwise hit.*
`,
	},
	{
		Name:        "Meeting notes",
		Description: "Attendees, decisions, and action items with owners.",
		Body: `# <topic> — <date>

**Present:**

## Decisions
*What was actually settled. Not the discussion, the outcome.*

## Discussion
*Points raised that didn't resolve into a decision.*

## Action items
- [ ] action — owner — date

## Open questions
`,
	},
}
