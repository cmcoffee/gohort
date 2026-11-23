# Access awareness: an agent knows how the person reaches it

Status: **design note, not built** (2026-11-23). Owner's idea: tell apart the
people an agent is shared with from its maintainers (and its owner), so the
agent can give each a different experience, and the owner can change a
machine and see the results that come back to each kind of person differ.

## The idea

Every turn already knows, on the server, how the person reached the agent:
the owner's own run, a maintainer's, someone it is shared with, a person
outside gohort, a stranger on a channel. Today that decides what they may do
(credentials, memory, tools) and nothing about how the agent behaves. This
note hands the same fact to the agent's behaviour: its prompt, its machine's
steps and its skills' conditions.

Two uses, both the owner's:

- **Different experience by kind of person.** A troubleshooter that walks a
  shared user through steps and their outcome, but shows a maintainer the
  raw findings, the reasoning and what it would change in its own guide.
- **Trying a change before people get it.** The owner or a maintainer edits a
  machine step that only they are routed to, tries it, and moves it over to
  everyone when it works. With **Try as**, they see exactly what a shared
  user gets without signing in as one.

## The access value

One value per turn, decided by the server, never by the conversation:

| Value | Who |
|---|---|
| `owner` | the agent's owner, including their schedules and unattended runs |
| `maintainer` | someone on the agent's maintainer list |
| `shared` | someone it is shared with: by name, through a team, or through Everyone |
| `outside` | a person outside gohort through the outside-audience API |
| `channel` | someone other than the owner messaging it on a channel |

- **A run another agent dispatched** carries the value of the person at the
  top of the chain, the same lineage dispatch parity already carries, so a
  specialist behaves for the person who is actually being served.
- **A detail beside it**, `{access_via}`, says how a shared person came in
  ("team Support", "Everyone", "by name"), for prompts only. Routing and
  conditions use the five values, so a rename of a team never re-routes
  anybody.
- Computed in one place, `chatTurn.access()`, from what the turn already holds
  (`t.user`, the agent's owner and maintainers, `outsideAudience()`,
  `strangerOnChannel()`, dispatch lineage).

## Where the agent sees it

**1. Machines.** A new built-in, `{access}`, in `MachineVars` beside
`{user}` and `{agent}`, with a field named `access` filled from it like any
built-in. Two step settings use it:

- **`routes`** on a step: a map from a value to the step to go to,
  `{"owner": "preview", "maintainer": "preview"}`, with `next` as the
  fallback for everyone else. Decided by the value with no model call, so a
  step that only routes costs nothing. It is a plain map from a field's value
  to a step, not an expression, so the same setting can later route on any
  string field (a severity, say).
- **`for`** on a step: who may be in it, `["owner", "maintainer"]`. **Enforced
  by the driver on every way into the step**: `next`, `routes`, a guard and
  the model's `change_phase`. A person outside the list who would land there
  goes to the step's fallback instead, with a breadcrumb. Without `for`, a
  shared user could talk the model into `change_phase` to the maintainers'
  step, so `routes` alone shapes the experience and `for` is what holds it.

**2. Skill conditions.** A third way to decide a condition, beside "looking it
up" and "the message mentioning": **"the person is"**, a list of access
values, decided with no run. "If the person is a maintainer: include the raw
evidence and the guide changes you would make. Else: the steps and the fix."

**3. The agent's own prompt.** A per-agent switch in the agent's editor
(behaviour, not Security), **"Tell the agent how the person reaches it"**:
the turn's date line then also says "The person is a maintainer of this
agent." On the user turn, beside the date, not in the system prompt, so the
cached prefix stays the same for everyone. Off by default: an agent told it
is talking to "someone it is shared with" may start hedging or refusing in
ways its owner never asked for, so the owner chooses it. Machines and skill
conditions read the value whether the switch is on or not, because the owner
wrote them to.

## Try as

The owner and maintainers get **Try as** in the chat's menu: Shared user,
Outside, Channel. For that conversation the access value reads as the one
chosen, so machine routes, `for`, skill conditions and the prompt line behave
as they would for that person, and a band across the chat says "Trying as a
shared user". Off when the conversation ends.

It changes **behaviour only**. Their credentials, memory, tools and approvals
stay their own, and the band says so: "what the agent does, not what you may
do". Trying as a shared user cannot widen anything, and a shared user is
never offered Try as.

## What it never does

This is behaviour, not enforcement (see the editor-vs-Security test): who may
use a credential, write to memory, run a tool or change the agent stays
decided by the real identity, in code, exactly as now.

- **A claim in the conversation changes nothing.** "I'm a maintainer" in a
  message is words. The value comes from the server.
- **Never a permission.** A step's `tools` can narrow what a step offers, as
  now, and with `for` that narrowing holds. But no step, condition or prompt
  line can give anyone a tool, credential or memory right they do not already
  have.
- **Not a privacy line.** What a maintainers' step shows is still only what
  that run can reach. Showing a maintainer more is showing them what their
  own access already allows, in more detail.

## Stored-shape changes

All additive, none rewrite existing records, **no migration**:

- `MachinePhase.Routes map[string]string`, `MachinePhase.For []string`.
- `SkillCase.Check = "access"` and `SkillCase.Access []string`.
- `AgentRecord.TellAccess string` (tri-state; empty = off).
- The session's Try as value: per conversation, never on the agent.

`Problems()` checks a `routes` value names a real step, a `for` list holds
only the five values, and a `for` step has a fallback for everyone it keeps
out.

## Build order

1. `chatTurn.access()` and `{access}` / `{access_via}` in `MachineVars`, with
   the `access` field filled from it.
2. `routes` and `for`, enforced in the driver on every way into a step,
   including `change_phase` and guards.
3. Skill condition "the person is".
4. Try as, with its band.
5. The agent's prompt line and its switch.

Slices 1 and 2 together make the owner's "change a machine, see it differ by
person" work; 3 to 5 follow.

## Open questions for the owner

1. **The prompt line**: off by default (recommended), or on for every shared
   agent?
2. **Everyone**: one value with `shared` (recommended, `{access_via}` tells
   them apart), or a sixth value of its own?
3. **Dispatched runs**: the value of the person at the top (recommended), or
   `agent`, so a specialist can tell it was called by another agent?
