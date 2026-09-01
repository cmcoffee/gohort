# Local commands as tools

How a binary an admin registered against a folder becomes tools an agent can
call. This replaces the flow shipped in v0.6.537, which works and is confusing.

## What is wrong now

Mapping a command is one job. It is currently spread across three surfaces:

1. **Map commands** — a chat on the store row. Builder probes the binary and
   proposes tools.
2. **Tools** — a picker on the same row, where you bind a proposal to the store.
   The binding is the approval.
3. The **global tools list**, where the proposal also shows up, attached to
   nothing, reading as orphaned.

Three problems follow from that shape.

**The result escapes its context.** A tool minted from `/opt/bin/cap` exists in a
list of every tool in the deployment, next to Jira wrappers and shell helpers,
with nothing on it saying which folder it came from or which binary it drives.
It is reachable-by-nothing until bound, which is exactly what "orphaned" means
and exactly what it looks like.

**The second step is unguessable.** Nothing about finishing a mapping
conversation suggests the work is inert until you open a different expander and
bind it. An admin who maps three commands and walks away has three orphans and
no tools.

**Five expanders, one subject.** Edit, Add command, Map commands, Tools,
Assigned to. Four of those are about the same folder's commands, and their
relationship is not visible from any of them.

The through-line: a tool minted here is *of* a command, and a command is *of* a
folder, and the interface models neither.

## The model

> A command is mapped into a **toolbox**. The toolbox is attached to the folders
> it may run in.

Three nouns, each owning the next.

**Store** — a folder. Already exists.

**Command** — a registered binary belonging to one store. Already exists.

**Toolbox** — what mapping produces: the actions one binary can perform, under
one name. New, and the reason the user asked for it: a binary is one thing, so
its actions should be one thing, sitting next to the command they came from
rather than scattered as loose tools in a global list.

A toolbox is **attached to stores**, plural. `capture-tools` mapped once from
`/opt/bin/cap` serves every folder of captures. That attachment is what says
where it may run, and it is the only grant involved — no per-store tool
authority, no separate approval concept.

This also answers "global to the store" better than the current answer does. The
tool is not global to a store; it is global to the **command**, and stores
reference it. A second folder of the same kind costs an attachment, not a
re-mapping.

## Why this ends the orphan

An orphan is a record nothing points at. Under the model, a toolbox is reached
from the command it was mapped from, which is reached from the store it belongs
to. There is no state in which it exists and nothing explains it — the worst
case is a toolbox attached to no store yet, which reads as *not attached*, on
the row of the command it maps, next to a button that attaches it.

It also never appears in the global tools list. It is not a tool of the
deployment; it is a capability of a folder.

## The interface

Everything about a folder lives on the folder. The store row expands to one
panel, not five:

```
Support bundles   /var/log/bundles   4 subfolders   3 users

  Commands
    decrypt      Decrypt bundle      /opt/bin/diag_decrypt
                 Mapped: capture-tools (4 actions)      [ Review ]
    unseal       Unseal archive      /opt/bin/unseal      two phases
                 Not mapped                              [ Map ]
                                                       [ + Add command ]
  Toolboxes in this folder
    capture-tools     4 actions, 3 enabled     from decrypt     [ Detach ]
    forensics         6 actions                mapped elsewhere [ Detach ]
                                                    [ + Attach a toolbox ]
  Who may reach it
    …
```

The mapping conversation opens from the command's own **Map** button and returns
to the command's own row. What it produced is on that row. Nothing has to be
found somewhere else afterwards.

Enabling actions is per-action on the toolbox, which the existing
`TempToolAction.Disabled` already supports — quarantining one action without
touching the rest. That is a better fit than the current all-or-nothing bind: an
agent probing a binary will propose things worth having and things worth
refusing, and the admin should be able to keep six of eight.

## The gap that has to be closed first

**Toolbox mode is HTTP-only.** `dispatchToolboxModeTempTool` builds a synthetic
tool with `Mode: TempToolModeAPI` and calls `dispatchAPIModeTempTool`;
`TempToolAction` carries `URLTemplate`, `Method`, `BodyTemplate`, `Headers` —
every field is an HTTP field. There is no way today to express "this action runs
a command line".

So the model needs `TempToolAction` to gain a shell kind: a `CommandTemplate`
alongside `URLTemplate`, and a dispatcher branch that runs it the way
`dispatchShellModeTempTool` does. This is the load-bearing change, and it is a
change to a shared type — it wants its own review, separate from the filestore
work that consumes it.

Worth doing regardless of this feature: a toolbox that can only wrap an API is
half a toolbox, and "several related commands under one name" is the same shape
as "several related endpoints under one name".

## Safety, and where it differs from today

A registered command execs with **no shell** — the binary, the folder and the
input are separate argv entries. A minted shell tool currently does not: its
`CommandTemplate` goes through `RunSandboxedShell`, so mapping a
carefully-argv-exec'd command produces something looser than the thing it maps.

The mapped actions should keep the original posture: argv, no shell, the
registered binary in position zero and nothing else. An action then declares its
arguments and cannot become a different command. This is a restriction the
current minting does not have and should.

What stays the same: probing only runs binaries an admin already registered, and
attaching a toolbox to a store is a person's act. The agent maps; it does not
grant.

## Staging

1. **`TempToolAction` learns shell.** A `CommandTemplate` field and a dispatcher
   branch, argv-only. Reviewed on its own — it is a shared type.
2. **Toolbox records, attached to stores.** Replaces `Store.Toolset` and the
   store-scoped tool table from v0.6.536-537. `Store.Toolboxes []string`;
   toolboxes stored once, referenced by many.
3. **The folder panel.** One expander replacing four, laid out as above.
4. **Mapping returns to the command row.** The conversation writes a toolbox
   against the command it was opened from, and the row shows it on return.
5. **Retire the global-list appearance.** A mapped toolbox is not a deployment
   tool and should not be listed as one.

Steps 1 and 2 are the substance; 3 and 4 are what the user actually asked for,
and neither is worth doing before the model underneath is right.

## What to do with what shipped

v0.6.536 (`Store.Toolset`) and v0.6.537 (mapping, store-scoped tools) are the
right pieces in the wrong arrangement. The probe/propose tools, the inert-proposal
rule and the conversation all survive into step 4. What goes is the loose-tool
representation and the separate Tools picker.

Reverting first is cleaner than migrating: nothing has been mapped in anger yet,
and carrying a store-scoped tool table forward into a model that does not have
one costs more than it saves.
