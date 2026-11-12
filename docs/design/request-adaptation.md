# Request Adaptation: design

**Status:** All four stages built on the `request-adaptation` branch
(2026-11-11): the registry with every learner on it, what it learns stored,
re-checked after 30 days and listed on the Models page with Forget, the output
maximum and sampling fields, refusals it cannot read shown rather than lost,
and probes at Test connection and when a tier's model changes.

## Problem

Model providers change what a request may carry, and each model differs. When a
model refuses part of a request, gohort has adapted four times, each time after a
failure had already reached the owner, and each time as its own learner:

| Learner | Where | What it adapts |
|---|---|---|
| Budget vs adaptive thinking | `noteAdaptiveThinking`, `core/llm_anthropic.go` | thinking `enabled`+budget → `adaptive`+effort |
| Thinking off ladder | `nextThinkingOff`, `core/llm_anthropic.go` | `disabled` → `between_tools` / low effort → nothing |
| `reasoning_effort` refused | `noteIfReasoningEffortRefused`, `core/llm_openai.go` | effort field → left out |
| Images refused | `(*retryLLM).learnNoImages`, `core/llm.go` | images → described by another model |

A fifth, `(*retryLLM).learnImageLimit`, learns how many pictures a model takes
in one request. Its step is a number, not a named rung, so it joins in stage 3
with the other numeric clamp (`max_tokens`).

They share one shape (match a 400, change one field, retry, remember), but:

- **Each is a copy.** A fifth means a fifth copy.
- **Each is learned by failing a real call first.** The owner's turn, check or
  assist is the one that pays.
- **Each forgets on restart.** Every restart pays again, once per model.
- **Nothing shows what was learned.** A model running on a fallback looks the
  same on the Models page as one running as configured.

A refusal that no learner recognises fails the call with only the provider's
message in a log.

## Goal

When a provider refuses part of a request, gohort adapts once, remembers it,
shows it, and never ends a call in an error because of a field it could have
changed or dropped. What it cannot map, it surfaces instead of guessing.

## The design

### 1. One registry of adjustable request fields (core)

```go
// An adjustable field of a model request.
type RequestField struct {
    Key      string   // "thinking.off", "thinking.shape", "reasoning_effort", "images", "max_tokens", "sampling", "tool_choice"
    Names    []string // what a refusal names it by, matched in a 400's message, any provider's wording
    Ladder   []string // the steps, top first, ending in "omit" (leave it out)
    Changes  bool     // whether a lower step changes what the caller gets
                      // (images described, a forced tool choice dropped): logged and reported
    Next     func(current string, refusal *APIError) (string, bool)
}
```

Each client (Anthropic, Bedrock's three APIs, OpenAI-compatible, Gemini) builds
its request from the field's current step for that model, as
`anthThinkingOff` does today.

The four learners become entries. New entries are cheap:

- `max_tokens`: a refusal naming a maximum ("max_tokens: 64000 > 32000") is
  clamped to it.
- `sampling`: `temperature` / `top_p` / `top_k` a model rejects are left out.
- `tool_choice`: a forced choice refused falls back to `auto`, reported, since
  the call may now answer without the tool.

### 2. A stored record per model

Kept for each instance, keyed by provider + the model id actually sent:

```go
type Adaptation struct {
    Field   string    // RequestField.Key
    Step    string    // the step it is on
    Since   time.Time
    Refusal string    // the provider's words that moved it, clipped
}
```

It's loaded at startup, so a restart does not pay again. The in-memory maps of
today's learners are replaced by it.

### 3. Re-checking

- **A learned step is forgotten when the tier's model changes:** new id, new
  record.
- **After 30 days**, the next call goes back to the top of the ladder.
  Providers relax limits as well as add them, and a fallback kept forever runs
  worse than it needs to.
- **Forget** on the page does the same at once.

### 4. Learning before the first real call

At save, and on Test connection, a few cheap probe calls:

- thinking off;
- an effort level;
- a tiny image;
- one tool call.

Each probe is a 1-token-answer request. The Test result lists what was adapted
("thinking off: between_tools"), so a new model's ladders are learned before
real work meets them.

### 5. On the Models page

Under each tier, a **Learned about this model** list: field, step, since when,
and the refusal that moved it, each with **Forget**. It's empty when the model
takes every request as configured.

### 6. Refusals it cannot map

A 400 that names nothing in the registry fails the call as today, and also
leaves a dashboard card with the exact refusal and the model. Nothing guesses a
change from free text: a model rewriting requests from an error message is
unbounded, and could quietly change what a call means. Each card is a candidate
for a new registry entry, so over time they become rare.

## Limits

- **Only a 400 adapts,** and only one that names a registered field. Never 401,
  403, 429 or 5xx.
- **Each retry is a different request** (the step moved), capped per call, so
  nothing loops.
- **Nothing is silent:** every step is logged once, stored with its refusal, and
  shown. A step that changes what the caller gets says so in the reply's
  diagnostics.
- **Calls that do not set a field are untouched:** a field the call does not use
  is never sent on its behalf.

## Stages (on a branch)

1. **Registry, behaviour unchanged.** *Built.* `core/request_adapt.go`: a field
   has a top step and a rule for the next step on a refusal of it as sent;
   each subject's step is kept in memory; `adaptedRetry` marks a refusal a
   field moved on. The four learners are fields (`thinking.shape`,
   `thinking.off`, `reasoning_effort`, `images`), their functions now readers
   over it, and their tests pass with their assertions unchanged (two test
   resets now call `forgetAdaptations`). The images field's subject is still
   the client instance, as its memory was; stage 2 keys it by model.
2. **Stored record and page list.** *Built.* Each move is stored
   (`request_adaptations`) with the refusal that made it, loaded on first use
   and kept in memory; a step older than 30 days is dropped on its next read.
   The images field is keyed by model (provider/model) instead of the client.
   Admin > Models has **Learned from model refusals**: one list for the
   deployment (model, what it refused, what is sent instead, since, its
   refusal) with Forget. One list rather than one per tier: the model id a
   request carries (Bedrock's `us.anthropic...`) often differs from what a
   tier's form says, so placing a row under a tier would be a guess.
3. **New entries.** *Built.*
   - `max_tokens`: a refusal that names a maximum (Anthropic's, OpenAI's and
     Bedrock's wordings) clamps the model's requests to it, on every Claude
     path and the OpenAI-compatible client.
   - `sampling`: temperature / top_p / top_k a model refuses are left out
     (OpenAI-compatible client; the Anthropic paths never send them).
   - The per-request image limit moved onto the registry, keyed by model.
   - One retry rule for any field a refusal moved, up to four in a call.
   - A 400 refusing a setting that no field reads is kept as a row ("something
     gohort cannot adapt to yet", with the model's words), and a dashboard
     card names the model for a week. Context overflows and content refusals
     are not rows.
   - `tool_choice` dropped: gohort never forces a tool choice, so there is
     nothing to adapt.
4. **Probes.** *Built.* `ProbeRequestFields` asks five short calls, each
   carrying one field (thinking off, an effort level, a sampling setting, a
   large output allowance, a one-pixel picture); a refusal is learned as any
   is. Test connection runs them after its own checks (the Offload model's
   too), shows each as a progress step, and ends with what was adapted to.
   A save that puts a tier (Worker or Lead) on another model probes it in the
   background, once per model per process (`ProbeOnce`), since fields save
   one at a time.

## Migration

**None.** Stored adaptations are new data, and an instance without any behaves
as today: every learner starts at the top of its ladder, as they do after a
restart now.

## Related

- **Connectors** (`core/service`) fail differently: a service's own API, not a
  model request. A failing connector offers ✨ Need help?, which opens its
  assistant with how its calls failed (`apps/admin/connector_fix.go`), and an
  agent's failed call says where help is. That path proposes changes for an
  admin to save; it does not adapt requests on its own.
