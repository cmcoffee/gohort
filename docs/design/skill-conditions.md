# Skill conditions: if / else if / else

Status: built, 2026-11-06 (v0.9.096). Replaced skill playbooks.

## Why

The playbook gave a skill conditional rules: "establish Y; if yes do Z, if no
do U". The framework ran the check and handed the agent only the arm that
applied. The mechanism was sound. The presentation was not: every skill row
said "add" under a "Playbook" column, the skill form carried a box of JSON,
and the editor asked for a "fact" as "one word, no spaces" and grew nested
rules and choice arms. The owner could not tell what it was for, and no skill
had a rule.

What stays is the part worth having: a check the agent cannot skip, settled
before it answers, with the skill's tools and the evidence that decided it.
What changes is the shape, now a flat list people already know how to read.

## What it is

A skill's **Conditions** list, built one row at a time with **+ Add
condition**:

- **If** (the first row, always): the primary condition. When it holds, the
  skill applies as written.
- **Else if** (any number after it): another condition, with its own
  instructions, used in place of the skill's when it is the first to hold.
- **Else** (at most one, last): instructions for everything the conditions
  above do not cover. Once there is an Else, "+ Add condition" is greyed, and
  says to change the Else to Else if to add another.
- **No Else**: when no condition holds, the skill does not apply.

A condition is decided one of two ways:

- **Looking it up**: a yes-or-no question and how to check it, answered by a
  short run with the skill's tools.
- **The message mentioning**: words, phrases, or `*.ext` for an attachment,
  decided from the message with no run at all.

Example:

```
Sync help
  Instructions   General sync troubleshooting...   [Edit]
  Conditions
    If       Is the customer on version 4 or later?   (looked up)
    Else if  Is the customer on version 3?            (looked up)   [Edit]
    Else                                                            [Edit]
```

## How it runs

- The cases are tried top to bottom; the first that holds wins.
- Every lookup question of the skill is answered in **one** check per turn,
  however many cases there are, and only when a lookup is reached: a message
  the If's mention already settles costs nothing.
- The agent gets the skill with the instructions of the case that applied,
  plus what the check found. It never sees the other cases.
- No condition holds and there is no Else: the agent is told the skill does
  not apply and why, and gets neither its instructions nor its sources that
  turn, and no "likely relevant" hint for it.
- A lookup that fails, or gives no clear yes or no, settles nothing: the skill
  does not apply, and the agent is told why, so it can ask rather than follow
  instructions meant for another case.

When it is settled:

- **The skill's triggers match**: before the model's first round
  (`checkConditionalSkills`). A trigger on a conditional skill is a rule about
  the turn, not a hint the model may decline.
- **The model consults the skill** (`read_skill`, a knowledge search or a
  document fetch): then.
- **Once per skill per turn**, cached, whichever door asked first. Not across
  turns: the answer can change between messages.

## Where it lives

- `core/skills.go`: `SkillCase`, `SkillRecord.Conditions`,
  `ConditionProblems`, `ConditionMachine` (the one check),
  `DecideConditions`, `ConditionFires`, and `ConditionRows` /
  `SetConditionRows` for the forms. The three skill tools take a `SkillGate`
  and hand over the skill as the gate chose it.
- `apps/orchestrate/skill_condition.go`: the turn's gate, run through the
  machine host (approval gate, activity line), and the pre-round check.
- Forms (Extensions › Skills, admin Skills): the Conditions rows field, and
  the instructions shown as a document with Edit (Outline or Raw) rather than
  in full. Builder: `skill_def`'s `conditions`, a JSON array.
- `core/ui`, generic: a rows field's `AddBlockedWhen` / `AddBlockedNote`, a
  column's `FirstDefault`, a `sections` cell (preview plus Edit), and
  `uiOpenDocEditor`, the modal Outline / Raw editor.

## Stored shape

```go
Conditions []SkillCase `json:"conditions,omitempty"`

type SkillCase struct {
    Kind         string   // "if", "else_if", "else"
    Check        string   // "" (look it up) or "mentions"
    Question     string   // look it up: the yes-or-no question
    How          string   // look it up: what to look at
    Mentions     []string // mentions: words, phrases, *.ext
    Instructions string   // else_if, else
}
```

No migration: no skill had playbook rules. An old export that carries a
`playbook` key imports with it ignored.

## Skills and machines

A skill is what the agent knows; a machine is what runs. The conditions are a
small machine (one check) that decides which instructions a skill hands over,
kept on the skill because skills have triggers and machines do not. Logic that
needs more than "which case applies" (several steps, state, outputs) is a
machine's job. Giving machines triggers, so a machine can do the deciding and
hand over a skill, is the next step if that need comes up.
