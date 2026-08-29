# Making the hidden prompt visible

Status: spec, 2026-08-29. Written after a night in which three separate bugs
turned out to be about prompt text nobody could see.

## Why

Three failures in one session, all invisible from any surface a person can open:

1. An agent broke both halves of the `[Style:]` rule (used "classic", emitted a
   U+2014) in six turns of small talk. Answering "was that rule even in the
   prompt?" took `cat -A` on `core/agent_loop.go` to check whether the line sat
   inside an `if`.
2. An agent reacted to a tool result it never relayed. The cause was an
   instruction that does not exist: nothing tells an agent the user cannot see
   tool output. Confirming a *negative* about the prompt is currently a grep.
3. A scheduled fire returned a table of finished posts, invented ids and a ✅
   against each, having called one read tool. The likely mechanism is history
   growth past the window, where llama.cpp context-shift silently drops the
   system prompt: persona and style live in history and survive, tool
   discipline lives in the system prompt and does not.

The third is the one that sets the shape of this spec. **A static list of
prompt rules would have actively misled us there.** It would have shown
`[Actions:]` present and correct, which it is, while the running system had
dropped it before the model saw it. The failure was absence at runtime, not
wrong content at rest.

So visibility has to mean *what was actually sent on this turn*, not *what the
source says we intend to send*.

## What already exists

More than expected, which turns most of this from invention into migration.

- `core/prompts.PromptBlock` — a registry of operator-visible fragments
  (`Key`, `Title`, `Category`, `Gate`, `Text`). Its own doc already states the
  intent: *"Read-only for now, this is the 'make the hidden prompts visible'
  step; editing/toggling layers on later, at which point this registry becomes
  the source the assembler reads instead of in-code constants."*
- `apps/prompts` — an admin section rendering that registry. Reached from admin
  only: `WebHidden`, no `HubTab`, self-registered via `RegisterAdminSection`.
- `apps/orchestrate/framework_prompts_registry.go` — registers **10** blocks,
  all `framework.*`, all from orchestrate.
- `core` already imports `core/prompts` (`core.go:25`), so registration from
  core needs no new dependency.

## The gap

`core/agent_loop.go` has 14 `systemPrompt +=` sites: one is the tool digest
(`BuildToolPrompt`, line 1940) and the other **13 are bracketed behaviour
clauses**. **None of the 13 are registered.** They include every rule that
failed above:

| Clause | Line | Gated on |
|---|---|---|
| Answering across tool rounds | 1961 | `len(tools) > 0` |
| Grounding contract | 1976 | `len(tools) > 0` |
| Capability-first | 1986 | `len(tools) > 0` |
| Grounding | 1987 | `len(tools) > 0` |
| **Actions** | 1993 | `len(tools) > 0` |
| Disagreeing with the user | 2002 | `len(tools) > 0` |
| Numbers | 2003 | `len(tools) > 0` |
| No false precision | 2011 | `len(tools) > 0` |
| Volatile facts | 2036 | `len(tools) > 0`, text varies with available search tools |
| **Style** | 2042 | always |
| Secrets | 2047 | always |
| Internal markers | 2052 | always |
| Round budget | 2059 | `maxRounds >= 10` |

(`BuildToolPrompt` at 1940 is the tool catalogue, not a behaviour rule. It is
already visible as the registered `framework.tools_directive` digest.)

The Prompts page therefore shows ten blocks and omits fourteen, with nothing
saying so. **A partial list is worse than no list**, because it reads as
complete. An operator checking "what are my agents told?" today gets a
confident, wrong answer.

## Stage 1 — register core's clauses (single source, no copies)

Mechanical, with one rule that decides whether it ages well: **the registry and
the assembler must read the same string.** Orchestrate's existing registry
duplicates its text as display metadata and accepts the drift risk. At 14 more
blocks, several of which encode incident history, that trade stops being worth
it.

So each of the 13 becomes a package-level `const` in `core`, unexported (no new
`core` exports, which matters: `TestCoreStaysUnderItsCeiling` is at its
re-baselined 2137 and a batch of new exports would trip it). The assembler
appends the const; an `init()` registers the same const with its `Gate`.

`Gate` is free-text and already exists for this. It should say the real
condition ("only when the agent has tools", "only when the round budget is 10
or more"), because a rule that is present for some turns and absent for others
is exactly what confused us.

`Volatile facts` needs care: its text is built at call time from which search
tools exist. Register the template and say so in `Gate`, or register both
variants. Do not register a string that is never sent verbatim.

## Stage 2 — the per-turn prompt record

The new capability, and the one that pays for the rest.

Per turn, record and surface:

- **system prompt size** in bytes, and the **clause keys included**
- **history size** in messages and chars, the **compaction budget**, and the
  **model window** (already computed and logged, e.g. `compaction check:
  history ~412 tokens, budget 131072, window 262144`)
- **provider-reported input tokens** (already returned, e.g. `input_tokens=46936`)
- a **headroom warning** when history plus prompt approaches the window

Do **not** store the full prompt text by default. It is 46KB+ per turn on
ordinary chat; a ledger of those is a storage problem and a mild disclosure
one. Store the digest above always, and the full text only behind an explicit
per-agent capture toggle, off by default. The digest is the part that must be
always-on, because the bug it catches is intermittent.

The headroom warning is the alarm that matters. The measured failure was
523,749 chars ≈ 131K tokens against a 131,072 budget: sitting exactly on the
line, which is why it was intermittent and why "the fire before it worked". A
surface that showed that number next to the window would have made a week of
archaeology into a glance.

Storage: `RunRecord` already carries `Steps` and `Raw` in an encrypted side
table read only by `GetRun`. The digest is small enough to live on the record
itself; full captures belong beside `Raw`.

Reach: the interactive path, dispatch, and scheduled fires all need it, and
scheduled fires need it *most* because nobody is watching. Note the existing
asymmetry recorded in the run-ledger notes: a recurring fire has no HTTP client
and tails no SSE ring, so its live card gets only a snapshot. The digest must
come off the run record, not off SSE, or scheduled runs get the least
observability exactly where it is needed.

## Rules are add/remove units, not editable text

Revised 2026-08-29 after review. The `PromptBlock` shape above is display-only
metadata; the operator model is a **rule registry** in the shape of the
markdown extension registry (`uiRegisterMarkdownExtension`): a base set that
ships with gohort, plus adds and removes on top.

The unit that matters is not the prompt text. It is the **rule**, and a rule
can have two halves:

```go
type Rule struct {
    Key      string // stable id, e.g. "style.no_em_dash"
    Title    string
    Category string // "Style", "Grounding", "Safety", …
    Gate     string // when it applies, in words
    Text     string // prompt text; empty for enforce-only rules
    Enforce  func(string) string // delivery-boundary transform; nil for ask-only rules
    Builtin  bool   // shipped, vs added by an operator
}
```

Tonight proves why they belong together. `[Style:]` asks a model not to emit an
em-dash or the word "classic"; `textutil.StripEmDashes` and
`textutil.StripFillerClassic` guarantee it at the delivery boundary. Those are
one rule expressed twice, in two files, with nothing connecting them. Split
like that, a future "turn off the classic rule" removes the sentence and leaves
the transform stripping words nobody asked it to, or the reverse.

So:

- Prompt assembly iterates enabled rules and appends each `Text`.
- The delivery boundary iterates enabled rules and applies each `Enforce`.
- Disabling a rule does both, atomically. That is the whole point.

Adds are text-only by construction (an operator cannot supply a Go function),
which is a feature: a custom rule is a request to the model, and the page can
say so plainly. Builtins with an `Enforce` are the ones that actually hold.

This supersedes the "editing, deferred" position below for whole-rule
add/remove. **Free-text editing of a builtin's `Text` stays closed**, for the
reasons in that section: each builtin encodes an incident, and a bad edit fails
silently. Removing a whole rule is coarse, visible, and auditable. Rewriting
one is neither.

### Open: what scope do adds and removes apply at

Deployment-wide, per-agent, or both. This decides the storage and most of the
UI, so it is the one question to settle before writing any of it.

Recommendation: **deployment-wide first**. It matches where these rules live
today (a global floor under every agent), it is one table, and the Prompts page
is already an admin surface. Per-agent disables are the natural second step and
the schema should leave room for them (key the store by scope, with `""`
meaning global), but shipping global-only avoids inventing a per-agent
override UI before anyone has asked for one.

## Stage 3 — editing, deferred on purpose

Not now, and the bar for later should be high.

`core/tunables.go` already draws this line and it should hold: *"only
operator-meaningful knobs register here. Internal quality thresholds stay code
constants, an operator can't judge a good value and a bad one silently corrupts
behavior."*

Prompt clauses are that, sharpened. Each encodes a specific incident (the
credential-solicitation failure, the volatile-price fabrications, the em-dash
tic). They interact: `Volatile facts` is built from which search tools exist,
`Grounding` depends on the date stamp on the user turn. And a bad edit produces
no error, only worse behavior weeks later.

Agents already have a persona for what an operator should shape. These clauses
are the floor under it, and a floor you can edit is not a floor.

The narrow exception worth considering first is a per-agent **disable** toggle
for a named block (specialised agents that genuinely should not get
`Capability-first`), which is auditable and reversible. Free-text editing is a
different thing and should stay closed until something concrete demands it.

## Principle this surface must not undermine

**Anything enforceable in code should not be prompt text at all.**

The `[Style:]` clause is the worked example. It asked a model not to emit a
token, was ignored, and is now enforced by `textutil.StripEmDashes` and
`textutil.StripFillerClassic` at the delivery boundary. The clause can be
deleted rather than displayed.

Exposing the prompt makes its contents feel like settings, which invites tuning
the words when the right move is often to delete them and enforce the thing. The
Prompts page should be read as a list of *candidates for deletion*, not a
control panel. Worth a line of copy on the page saying so.

## UI

The Prompts section must **not** be collapsed in the admin menu. It is a
reference surface people go to when something is behaving oddly, and a
collapsed entry reads as "advanced, probably not for you", which is the
opposite of the point. Top-level, expanded, visible in the menu without a
click.

Within the page, group by `Category` and show `Gate` next to each block, since
"is this rule live for this agent right now?" is the actual question being
asked.

## Order

1. Stage 1 (register core's 13). Makes the existing page honest. Small.
2. Stage 2 digest + headroom warning. The diagnostic that pays for itself.
3. Full-text capture behind a toggle.
4. Stage 3 only if a real need appears.

Stage 1 without Stage 2 is worth shipping alone. Stage 2 without Stage 1 is
also worth shipping alone, and is the more valuable half.
