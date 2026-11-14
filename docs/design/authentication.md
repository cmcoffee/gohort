# Authentication: password and sign-in providers

Status: proposed (2026-11-14). Nothing built. Follows the live trial of
Microsoft sign-in (docs/design/oidc-signin.md, built as one Entra
directory), which comes first.

## Why

Microsoft sign-in is built as one feature: one settings record, one
button, the Entra checks built in. It is authentication, and the next
providers people ask for are the same thing again: Google Workspace, Okta,
and the identity servers people run themselves (Keycloak, Authentik,
Zitadel). Rather than a section per provider, the admin page gets one
**Authentication** area: how passwords work, and a list of sign-in
providers, each with its own settings and button. An account can connect
more than one.

## What carries over unchanged

| Piece | Where | Why it already fits |
|---|---|---|
| The protocol | `core/oidc` `Provider` (Start / Finish) | It takes an issuer, a client ID and a credential, and knows no provider. The Entra check (`Tenant` against `tid`) is one optional field. |
| Links | `auth_sso_links`, keyed `<issuer>\|<permanent ID>` | The issuer is in the key, so a Google or Okta identity links the same way, and every existing link stays valid. |
| The routes and their checks | `core/auth_sso.go` | The browser cookie, the single-use state, the 10-minute life, linked-account-only sign-in and the New people rule apply to any provider. |
| The fake provider | `core/oidc/oidctest` | Already takes any issuer; a test per provider type adds its claims. |

## Provider types

Three, kept few on purpose. A type decides the issuer and the extra check
on who may sign in; everything else is the same code.

| Type | Issuer | Who may sign in | Client credential |
|---|---|---|---|
| Microsoft (Entra) | `https://login.microsoftonline.com/<tenant>/v2.0`, from the tenant ID | That directory only: `tid` must equal the tenant. `common`, `organizations` and `consumers` refused, as now. | Certificate made here (recommended) or client secret |
| Google | `https://accounts.google.com` | One Workspace domain: `hd` must equal it. A personal Gmail account has no `hd` and is refused. | Client secret |
| OpenID Connect (any other) | Typed in: Okta (`https://<org>.okta.com/oauth2/default`), Keycloak, Authentik, Zitadel | Whoever that issuer authenticates. The admin page says so plainly: the provider decides who exists. | Certificate or client secret |

The permanent ID is `oid` for Entra (as built) and `sub` for the others,
which is permanent and per user there. Never the email.

GitHub is not on the list: it speaks OAuth without an ID token, so it would
need its own adapter (the user API, keyed by numeric user ID). Later, if
asked for.

## Settings: a list, read in place

Each provider is one record:

```go
type Settings struct {
	ID         string // short, stable, in URLs: "microsoft", "google", "okta"
	Type       string // entra | google | oidc
	Name       string // the button's text: "Sign in with Contoso"
	Enabled    bool
	Tenant     string // entra
	Domain     string // google: the Workspace domain
	Issuer     string // oidc: typed in
	ClientID   string
	Credential string // certificate | secret
	NewPeople  string // refuse | approval | let_in, per provider
}
```

New People is per provider because trust is: everyone in the company's
Entra directory may be fine to let in, while a self-run Keycloak realm may
not be.

**No migration.** Today's record stays where it is (`auth_sso` /
`settings`) and is read as the provider `microsoft`, type `entra`; its
certificate and secret keep their keys. Other providers are stored beside
it under `provider:<id>`, their secrets under `certificate:<id>` and
`client_secret:<id>`. Nothing that exists is rewritten. Two providers with
the same issuer are refused, since their links would be one set.

Removing a provider deletes its settings and secrets. Its links stay, listed
as "provider removed" so they can be disconnected, and adding the same
issuer back makes them sign in again.

## The flow, for many providers

- **Start**: `GET /login/sso/<id>`. Bare `/login/sso` stays the first
  provider, so a bookmarked button keeps working.
- **One callback for all**: `GET /login/sso/callback`. The pending sign-in
  carries the provider's ID, so the redirect URI registered with every
  provider is the same one, and the callback cannot be pointed at a
  different provider than the one the sign-in began with.
- **Login page**: a button per enabled provider, in the admin's order, above
  the password form.
- **Account page**: a Connect button per enabled provider, and the
  connected identities, each with its provider's name.

## Password sign-in

The setting the Microsoft build left out, once there is a second way in:

| Setting | Values | Default |
|---|---|---|
| Password sign-in | On / Administrators only / Off | On |

- **Administrators only**: everyone else signs in through a provider;
  administrators keep their passwords as the way in when a provider or its
  registration breaks.
- **Off**: offered only when the desktop app can sign in through a
  provider (below), and only while at least one administrator has an
  identity connected from an enabled provider. Turning off the last way an
  administrator can get in is refused, and saying why.
- When password sign-in is not On, the forgot and reset pages refuse the
  accounts it no longer covers ("this account signs in with Microsoft"), so
  a reset email does not hand them a password.
- The command line (`gohort --setup`) can always set a password and turn
  password sign-in back on: the way back in when everything else is wrong.
- Public signup (the password form's own) follows the same setting: off
  when password sign-in is not On.

## Admin layout

On People, where Sign-in and Microsoft sign-in are now:

1. **Authentication**: password sign-in, public signup, session lengths,
   lockouts, and the URL-key setting (the current Sign-in section's fields,
   moved, plus Password sign-in).
2. **Sign-in providers**: a list, one row per provider with its type,
   whether it is on, and its button text. **Add a provider** asks for the
   type, then opens that type's form: the redirect URI to register, the
   fields that type needs, its certificate or secret, New people, and a
   status line saying what is missing. Rows reorder (the login page's
   order) and remove.
3. **Connected identities**: the current table, with a Provider column.

The Microsoft sign-in section becomes the `microsoft` row; nothing about it
changes for someone already set up.

## Code

- `core/oidc`: `Settings` gains `ID`, `Type`, `Domain`, `Issuer`;
  `LoadProviders`, `SaveProvider`, `RemoveProvider`; per-ID certificate and
  secret; `Settings.Issuer()` by type; the `hd` check beside the `tid` one.
- `core/auth_sso.go`: the start route takes the ID, the pending sign-in
  carries it, the login page lists the buttons, Password sign-in is read
  there and in the forgot and reset handlers.
- `apps/admin`: the Authentication and Sign-in providers sections; the
  current page_sso.go becomes the provider form.
- `apps/account`: a Connect button per provider.
- Core's export count stays where it is: all of it is in `core/oidc`, or
  unexported beside the handlers that use it.

## Testing

The fake provider, per type: Entra's `tid`, Google's `hd` (a Workspace
account in, another domain out, a personal account out), a generic issuer.
Then: two providers on at once, each sign-in finished against its own; a
callback whose state names one provider and whose code came from another;
the old single record read as `microsoft` with its links signing in
unchanged; Administrators only refusing a non-admin's password and reset;
Off refused while no administrator has an identity connected.

## Steps

1. The provider list in `core/oidc`, read in place, with the types and their
   checks. The routes take the provider. Login page buttons.
2. Admin: Sign-in providers (add, edit, reorder, remove) and the Provider
   column; the Microsoft section folded in.
3. Account page: a Connect button per provider.
4. Google, tried live against a Workspace domain.
5. Password sign-in: Administrators only, the reset refusals, the lockout
   guard.
6. Off, once the desktop app signs in through a provider.

## Open questions for the owner

1. **The desktop app.** It keeps its session in its own cookie jar behind a
   loopback proxy (gohort-desktop/proxy.go), so a sign-in that goes out to a
   provider and back lands outside it. The likely path is the system
   browser: the desktop opens the sign-in there, and the callback hands the
   session back to the desktop through its loopback port. That needs its own
   design and a trial. Until then Off is not offered, and Administrators
   only leaves non-admin desktop users without a way in: is that
   acceptable, or should Administrators only wait for the desktop too?
2. **Google's personal accounts.** Refused by default (no Workspace domain).
   Is there a case for allowing them, for a deployment open to the public?
3. **Order of work.** This after the Microsoft live trial, as proposed, or
   Google first because it is easier to try?

## Later

- Groups to teams: the provider's group claim decides gohort teams and app
  access.
- Provisioning (SCIM): the provider creates and deactivates accounts, which
  also ends sessions when someone is turned off at the provider.
- Signing out at the provider too (RP-initiated logout).
- MCP and API access with provider-issued tokens, in place of gohort keys.
- A GitHub adapter.
