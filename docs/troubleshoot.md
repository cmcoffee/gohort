# The troubleshooting machine

Status: built (v0.6.813). `extras/troubleshoot.machine.json`.

A decision tree for taking a symptom on a system to a verified fix or a clean escalation. It is an
agent machine because every node needs judgement about what the evidence says, and a machine already
provides the two things a decision tree needs: a declared set of branches per node, chosen by the
model through a structured output, and routing the model cannot invent. The leaves are resident
phases, where the conversation comes to rest.

## Shape

```
intake ──► network ─────┐
   │  ├──► service ─────┤
   │  ├──► performance ─┼──► propose_fix ──► stage_fix ──► apply ──► verify ──► resolved
   │  ├──► storage ─────┤        │                                       │
   │  └──► access ──────┘        └──► escalate ◄─────────────────────────┘
   └──────────── every arm may send the conversation back to intake ─────────
```

- **intake** (transient, thinks, no tools) sorts the symptom into one of five domains and records the
  system, the symptom, and `working_means`: the one check that will prove it fixed. It does not
  investigate. The verify step runs exactly that check later, so intake is told to make it something
  a command can test.
- **network / service / performance / storage / access** (transient, think, inherit the agent's tools)
  each run a fixed order of checks and stop at the first one that is wrong. Each ends by choosing one
  of three exits: `propose_fix` when it can name a single command and a way back, `escalate` when the
  fix needs access, a change window, or a decision it does not have, or `intake` when the domain was
  wrong. Each declares the same output (finding, evidence, ruled_out, fix_command, rollback), and
  contributes its evidence to the `gathered` accumulator, so an escalation carries everything from
  every arm the conversation passed through.
- **propose_fix** (resident, no tools) states the fix, the evidence, the rollback and the check, and
  waits. The person's yes is the decision: a guard reads it and moves to `stage_fix`. Declining or
  handing off leaves by `change_phase`, bounded by `exits_to` to escalate and intake.
- **stage_fix** (transient, no tools, thinking off) writes down the approved command exactly as last
  stated. It exists because a resident phase cannot declare an output, and because what runs should be
  what was approved, not the domain step's first draft of it.
- **apply** (tool step, no model) runs `{state:stage_fix.fix_command}` through `run_command` and
  hands to verify.
- **verify** (transient) runs the check from intake and reports `fixed`. Nothing else counts: not the
  fix command's exit code, not a unit that says active while the symptom persists.
- **resolved** and **escalate** (resident) write the outcome down as a finding titled with the
  symptom, so the next occurrence starts from a search hit. A new symptom raised in either trips a
  guard back to intake.

## What the shape decides on purpose

**The model never picks a destination.** It picks a value from `choices`, and the edge is declared.
Adding a branch is adding a phase and a choice, not editing a prompt.

**Nothing changes a system without a human node in front of it.** propose_fix is resident and the
machine is not unattended, so the tree cannot fire the apply step on its own. The command that runs
is the one restated after the yes.

**Each domain stops at the first failing check.** A step that keeps going after finding the problem
produces a list of possibilities; a step that stops produces a finding. When the cause lives in
another domain (a service that is down because a disk is full), the step says so and sends the
conversation back to intake rather than fixing it from where it stands.

**Clean checks are recorded.** `ruled_out` on every domain step and the instruction to include clean
checks in the handoff exist so the next person does not walk the same ground.

## Tools

The domain steps name no tools and inherit whatever the agent carries, for the reason the
investigation recipe does: the exec tool is `run_command` on a servitor agent and may be something
else elsewhere. The apply step is the exception (it calls `run_command` directly, with no model)
so the machine expects to be attached to an agent that has it. Narrow a domain step under "How this
step runs" if it should not reach everything the agent can.

## Loading it

```sh
curl -sS -b cookies.txt -X POST http://127.0.0.1:8181/orchestrate/api/machines \
  -H 'Content-Type: application/json' -d @extras/troubleshoot.machine.json

curl -sS -b cookies.txt -X POST http://127.0.0.1:8181/orchestrate/api/agents/<agentID> \
  -H 'Content-Type: application/json' -d '{"machine": "<machineID>"}'
```

Or ask Builder: *"load the machine in extras/troubleshoot.machine.json and attach it to <agent>"*.
Then open a new session; the machine is pinned per session at creation.
