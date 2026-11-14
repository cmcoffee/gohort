# Retiring "Reachable over MCP"

Status: built (2026-11-13), the no-migration shape. Decided with the owner:
an agent shared with someone is reachable from their own key; an
outside-audience key reaches only its owner's own agents (below). Whether
serving people outside should need an administrator's approval is left open.

## Why

Every agent has a "Reachable over MCP" switch (`AgentRecord.MCPExposed`,
agent page, Access & visibility). It reads as the thing that lets an
outside client (Claude Desktop, a voice platform) reach the agent. For
the owner's own agents through a key made today, it does nothing:

- **Ticking the agent on the key is already the consent.**
  `ResolveExternalAgentGranted` passes an agent when the switch is on
  *or* the key explicitly lists it and it is the key owner's own
  (`externallyReachable(a) || granted && ownAgentFor`). The key editor
  offers all of the owner's agents whatever the switch says.
- **Turning it on widens nothing.** Every key made now has a scope
  (`MintAccountTokenScoped`). A scoped key reaches only the agents it
  lists: `keyMayAsk` refuses the rest over MCP, and `gateTarget` refuses
  them over `/v1`.

So the switch costs a setting on every agent and an approval
(`agentPublishNeedsApproval`) that a key tick goes around, which
`external-access.md` already lists as a gap. The gates that matter are
the admin's per-user feature grant (MCP, Model API), the key's own list
(deny-by-default), and sharing.

## What the check found (verified in code, 2026-11-13)

### Callers with no key: none reach an agent

| Surface | Without a personal key |
|---|---|
| `/v1` | Refused outright (`gateFeature`: "takes a personal access token"). |
| MCP, desktop or bridge key | Refused in `authorize`. |
| MCP, signed-in browser session | Gets past `authorize`, but `AllowsTool` is false for a nil token, so `tools/call` refuses every tool, `ask_agent` included. |
| MCP, orchestrate not loaded | The `MCPAgentExposed` fallback branch in `askAgent`. Unreachable in a normal deployment, because orchestrate registers the resolver at startup. |

Nothing needs a rule of its own for keyless callers.

### Keys with no scope: none can be made, old ones may still exist

- **None can be made now.** The only mint call outside core is
  `MintAccountTokenScoped` (`apps/account`), and `SetAccountTokenScope`
  refuses a nil scope.
- **Old ones may exist.** Keys made before scoping (bc6bee3a, 2026-07-23)
  have no scope, and they reach every agent with the switch on, plus
  every feature and tool (`AllowsFeature`, `AllowsTarget` and
  `AllowsTool` all pass a nil scope).
- **They are on a clock.** The key lifetime (4ba68d87, 2026-10-16,
  default 90 days) gives every key with no deadline one, the first time
  it is seen after that. Unless an administrator set the lifetime to 0 or
  raised it, every such key stops working by about **2027-01-14**.
- **Whether any are in use can't be told from here.** The database isn't
  read in these sessions. The live log only goes back to 2026-11-09. It
  shows no `/v1` use of an unscoped key ("predates scoping" is logged
  there), and the MCP path does not log it at all.
- **The owner can check.** The Account page marks an unscoped key with a
  "legacy" badge.

### Other places the switch is read

| Where | What it does | After |
|---|---|---|
| `externallyReachable` | The OR before a key's grant | The key's grant alone, for own agents and for agents shared with the key's owner |
| `ResolveExternalChannel` | A channel's agent must have it on | Removed. The key's `channel:` target is already checked after the resolve (`gateTarget`). |
| Key editor (`external_agents.go`) | Lists a shared agent only when its owner turned it on, and a framework agent only when it is on | Shared agents are listed whenever they are shared with the user. Framework agents stay off the list except the main agent, as now. |
| `bindAgentsToMCP` | Old scoped keys get the agents they reached, read through the switch | Keeps working until every key is bound: it reads the stored field for that one purpose |
| Approval (`agentPublishNeedsApproval`, `approveAgentPublish`) | The administrator approves turning it on | Gone with the switch. The administrator's say is the per-user MCP and Model API grant. |
| `agent_move.go`, `userdata.go`, privilege card | Blocks moving the agent under another, counts it as shared, shows it | Dropped |
| `apps/mcpserver` messages | Tell the caller to turn the switch on | Point at the key's Access instead (most already do) |

## The proposal

1. **A key reaches exactly what it lists.** For the owner's own agents,
   that is already true. For an agent shared with the owner, the key may
   list it while the share stands. `userCanRunSharedAgent` is checked at
   every call, so unsharing cuts the key off at once. Sharing is the
   sharer's consent. The other way to see it is that the person can
   already copy the agent's answers out of the web page.
2. **Channels need only the key's tick.**
3. **People outside gohort are served only by the agent's owner.** An agent
   shared with someone is theirs to use, from gohort or from their own
   client, but serving it to people who are not gohort users is another
   form of sharing, and only the owner makes it. Before, the sharer's switch
   stood in the way of a recipient's outside-audience key; with the switch
   gone, the run itself refuses it (`outsideRunRefusal`: an outside
   person's turn on an agent that is not the key owner's own).
4. **The switch comes off the agent page** and the privilege card, the
   approval kind goes, and the move, shared-count and message sites drop
   it.
5. **The stored field stays in records** (`omitempty`) and is read in
   two places only, until they retire:
   - **unscoped keys**: they keep reaching what the switch says, so
     nothing an old key does today stops at deploy;
   - **`bindAgentsToMCP`**, so an old scoped key still gets the agents it
     reached.

   Both readers are deleted after 2027-01-14, when the key lifetime has
   retired every unscoped key. This is the no-migration shape: nothing
   is rewritten at deploy.

**The alternative, a migration.** At deploy, convert each unscoped key
into a scoped one listing what it reaches now: the features, the agents
with the switch on and the tools. The reader can then go at once.
Recommended only if the Account page check finds unscoped keys that
matter and a lifetime of 0 means they would otherwise live forever.

## What changes for people

- **Owners:** one less setting. A key's Access is where reach is
  decided, as it already is in practice.
- **Recipients of a shared agent:** can tick it on their own key without
  asking the sharer to flip anything.
- **Administrators:** lose an approval that guarded nothing a key tick
  could not already do. They keep the per-user MCP and Model API grants.

## Testing

- A key reaches an own agent it lists and refuses one it does not, with
  the stored field on and off: the field changes nothing.
- A shared agent: tickable while shared; refused at the next call once
  unshared; never listed for a user it is not shared with.
- A channel: reachable with the key's `channel:` tick alone; refused
  without it.
- An unscoped key still reaches what the stored field says.
- A browser session still cannot call an MCP tool.
- The agent page and privilege card no longer show the switch.

## Open question for the owner

Should ticking an agent on a key need an administrator's approval
instead, for deployments that want that control? The proposal says no:
the per-user MCP and Model API grants already decide who may use outside
clients at all.
