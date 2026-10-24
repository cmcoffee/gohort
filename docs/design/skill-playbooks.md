# Skill playbooks

Status: built v0.6.814–818. `core/skills.go` (`PlaybookRule`), the resolver in
`apps/orchestrate/skill_playbook.go`, the visual editor in
`apps/extensions/skill_playbook_editor.go`. Tests: `core/skill_playbook_test.go`,
`apps/orchestrate/skill_playbook_test.go`, `apps/extensions/skill_playbook_editor_test.go`.

## The problem

A skill could already say "when asked about X, establish Y first; if Y then Z,
otherwise U", as a sentence in its instructions. A model that already believes it
knows Y goes straight to Z. The instruction reads like a rule and behaves like a
suggestion, which is the same weakness `AgentRecord.Rules` has and `Guardrails`
does not.

A machine enforces the ordering, but a session is pinned to one machine, so an
agent with twenty such rules cannot have twenty machines. A playbook is the
cheaper primitive for the same shape: it runs inside an ordinary turn.

## What a rule is

```json
{
  "when":  ["stuck order"],
  "fact":  "queue_draining",
  "how":   "Read the consumer lag for the orders queue over the last five minutes.",
  "then":  "Look at the consumer: its log, restart count, lag trend.",
  "else":  "Look at the broker: connectivity from the consumer host, partition state, disk."
}
```

- `fact` is one word: it becomes a declared output field, so it is decoded, not
  read out of prose.
- `how` is the instruction for establishing it, run with the skill's own tools.
- `then` / `else` are the arms of a yes-or-no fact. For a many-way branch set
  `"type": "choice"` with `"values"` and a `"cases"` map.
- An arm may be prose, or another rule via `then_rule` / `else_rule` /
  `case_rules`. **Two levels at most**: deeper than that is a machine, and the
  author should write one.
- `when` limits the rule to matching turns, matched like the skill's triggers.
  Omit it and the rule applies whenever the skill does.

## What actually happens

When a playbook skill applies to a turn, **the framework establishes each rule's
fact before the model's first round**. The rule compiles to a one-phase unattended
machine carrying the skill's tools, with the fact as a required declared output; it
runs through the turn's own machine host, so the step has the approval gate and a
line of activity the person can see. The model then receives the skill's
instructions plus:

```
**Playbook** (Disk check — established for this turn; follow what applies):

**Established:** root_full = false — tmpfs 7.7G 0 7.7G 0% /
**So:** Say the root disk is fine and give the free space.
```

Only the arm that applies. The model never sees the branch it did not earn, and
cannot skip the check, because the check ran before it was asked anything.

The step also reports an `evidence` field (the one line that decided the fact)
so an arm that needs the number the check found has it without running the check
again.

## When it fires

**On a match, not on a consult.** A skill's triggers, or any rule's `when`,
matching the turn is what fires it. Ordinary skill activation is model-driven (it
reads the description and decides whether to `read_skill`), and a trigger match is
only a hint: deliberately, because injecting prose nobody asked for is what
triggers avoid. A playbook is different: it DOES something, and "when asked about
X, establish Y" is a rule about the turn rather than a suggestion.

**So give a playbook skill triggers.** Without them it only runs when the agent
chooses to consult the skill, which is the thing the playbook exists to stop being
optional. The skill list says so, and so does the `skill_def` reply.

## When it cannot

A rule whose fact cannot be established (the check errored, reported nothing, or
answered something that decides neither arm ("probably")) is handed to the model
as prose instead: establish this yourself, and here are both arms with their
conditions. Less than enforcement, more than silence.

A rule with problems (no arm, a fact with spaces, nesting too deep) is **skipped
entirely**, never run. Validation lives at the authoring doors, not in the store,
so the visual editor can save a rule half-built without it firing.

## Authoring

**Visual editor**: Extensions › Skills › the Playbook column (it shows "add",
"1 rule", "3 rules" and is itself the link), or the **Playbook Editor** button in
the skill's Edit panel. One form per rule, asking each part at the point of choice,
with the rule read back as a sentence under its heading.

**JSON**, the Playbook field in the skill's Edit panel reads like Instructions: a
preview with an **Edit** button that opens it in a modal. Admin's skill form has
the same textarea.

**Builder**: `skill_def` takes a `playbook` argument (a JSON array) alongside
`attach_to_agents`. A skill no agent allows is invisible, so attach it in the same
call; the reply says whether it did.

## Cost

One worker call per matching rule, on the turn the rule fires. A three-rule
playbook whose triggers all match is three calls before the model answers, so keep
`when` narrow on rules that are not always relevant.
