# Security

Gohort runs code an LLM wrote, holds credentials on your behalf, and answers on a network
port. Those three together are the whole security story, and this file is where its terms
are written down.

## Reporting a vulnerability

Use GitHub's private vulnerability reporting:

**https://github.com/cmcoffee/gohort/security/advisories/new**

Please do not open a public issue for a security problem. This is a project that holds
people's API keys; a public issue is a disclosure with no fix available yet.

Useful to include: the version (`gohort --version` or `version.txt`), the platform, whether
the deployment is single- or multi-user, and the smallest sequence that reproduces it. If
it involves a tool or an agent, the definition matters more than the transcript.

What to expect: one maintainer, working best-effort. You will get an acknowledgement, and
then either a fix on `main` or a plain explanation of why the behavior is intended. There
is no bounty. Credit in the advisory if you want it.

## Which versions get fixes

Pre-1.0. Fixes land on `main` and ship in the next version; nothing is backported. If you
are running a build from some weeks ago, the answer to almost any report will begin with
"update first" — the version number moves several times a day.

## The trust model

Most of this codebase is an argument about who is trusted with what. Stated plainly:

| Party | Trusted? |
|---|---|
| **The operator / admin** | Yes, completely. They author tools, approve credentials, and configure the deployment. Nothing here defends against a hostile admin, and that is deliberate — it is their machine. |
| **Users of a deployment** | Semi-trusted. Per-user data isolation, per-user credentials, explicit cross-user sharing. A user is not an admin and cannot become one by asking. |
| **The model** | **Not trusted.** This is the spine of the design: approval gates on tools that need them, capability grants per tool, credentials the model cannot read, an independent guardrail warden that never saw the conversation, dispatch allowlists that cannot widen along a chain. |
| **Tool output and fetched content** | Not trusted. Anything a tool brings back — a web page, an API response, a file — is data, never instruction, and is scanned on the way in. |
| **Peers** | Authenticated and semi-trusted. A peer is reached by key, gated per capability, and a shared recipe runs against the *recipient's* tools and credentials: the recipe travels, the authority does not. |

The practical consequence: a model that has been talked into something should still be
unable to reach a credential it was not granted, a tool it was not given, or another user's
data. When it *can*, that is a vulnerability and worth reporting.

## Confinement: what "sandboxed" means on your host

Shell and script tools run under one of three backends, and the third one is not a sandbox:

- **bubblewrap** (Linux) — a mount namespace. The workspace is writable, most of the
  filesystem is read-only or absent, network follows the caller's grant.
- **Seatbelt** (macOS, `sandbox-exec`) — a deny-by-default profile with the workspace
  writable.
- **unconfined** — no bwrap on a Linux host, or a macOS host where `sandbox-exec` is
  missing or refused the probe profile. **Shell tools then run at the daemon's own
  privilege.**

Unconfined is a real, reachable state, not a theoretical one. Check which you are in at
**Admin → System Status**, or call `GetSandboxStatus()`. To refuse rather than degrade, set:

```
GOHORT_SANDBOX_REQUIRED=1
```

With that set, a tool that cannot be confined does not run. Without it, the deployment logs
the degradation once and keeps working, because the alternative on a host with no sandbox is
that no tool works at all. Which of those you want is a deployment decision, so it is yours
to make rather than ours to assume.

## Credentials

Two kinds, and the difference is what a script can see:

- **Ordinary credentials** are handed to a tool that was explicitly granted them
  (`secret:<name>` in its declared capabilities). The script receives the secret value.
- **Secured credentials** never leave the server. A script asks for a *call*
  (`fetch_via:<name>`), the host applies the auth, and only the response comes back. Being
  bound to a declaring tool, and to the users the credential is shared with, is enforced at
  the same point. Code the model wrote cannot read the key it is using.

At rest, credentials and all other stored data are encrypted with a key derived from the
host, so the database file on its own is not enough to read them. The corollary matters for
backups: a copied database does not open on a different machine.

## Deployment posture

Gohort binds a port and serves a dashboard. A few things worth deciding before it faces
anything wider than localhost:

- Put TLS in front of it, or bind it to a local interface and reach it through something
  that does.
- The admin surface takes an IP allowlist (`admin_allowed_ips`, comma-separated CIDR). Set it
  if the deployment is reachable from more than your own machine.
- Multi-user means real accounts with per-user isolation, not a shared password. Create
  accounts rather than sharing one.
- Local-first is the default for a reason: with a local model, prompts and tool output never
  leave the host. Wiring a hosted provider is a deliberate choice, and the per-agent privacy
  controls exist so it can be made per agent rather than globally.

## What is and is not a vulnerability

**In scope** — anything that crosses a boundary the design claims to hold:

- A model, tool, or script reaching a credential, tool, or user's data it was not granted.
- Escaping confinement on a host that reports `Confined: true`.
- A capability grant that widens along a dispatch or peer chain instead of narrowing.
- Authentication or session handling that lets one user act as another, or a non-admin reach
  an admin surface.
- A secured credential's secret becoming readable by tool code.

**Out of scope** — real behavior, but working as designed:

- Prompt injection that makes an agent *say* something wrong or unhelpful. Models can be
  talked into nonsense; that is why nothing downstream trusts their output. Injection that
  crosses a capability boundary **is** in scope, and the distinction is the whole point.
- An admin authoring a tool that does something dangerous on their own deployment.
- Running unconfined on a host with no sandbox backend, when that state is reported
  correctly and `GOHORT_SANDBOX_REQUIRED` is unset. A sandbox that silently reports
  `Confined: true` while not confining is in scope.
- Resource exhaustion from a schedule or agent the operator configured themselves.
- Findings from a scanner with no working exploit against a supported configuration.
