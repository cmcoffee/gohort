# Sign in with OpenID Connect: single-tenant Entra first

Status: built, steps 1 to 4 (2026-11-14); the live trial against a real
tenant (step 5) is next. Where the build differs from this design is in
"As built" at the end.

## Why

Gohort signs people in with a username and password only. An organization
that already runs an identity provider wants its people to sign in with
the account they already have: no second password to forget, and the
provider's MFA, conditional access and offboarding apply to gohort for
free.

OpenID Connect (OIDC) is the protocol nearly every provider speaks
(Microsoft Entra ID, Google, Okta, Keycloak, Authentik). This build adds
OIDC, and the first provider it is tried against is a **single Entra
tenant**: one organization's work accounts, nobody else's. The code is
generic OIDC; Entra is the first configuration, not a special case.

## What exists already (verified in code)

| Piece | Where | Used for |
|---|---|---|
| One place mints a session | `AuthCreateSession(db, username)`, `core/auth.go` | Password login checks the password, then calls this and sets the cookie. SSO ends in the same call, so cookies, `AuthMiddleware`, app access and groups are untouched. |
| Issuer key sets | `core/jwks` `Verifier` | Reads the discovery document, caches the signing keys, checks signature, `iss` and `aud`. This is the ID token check. |
| Authorization code + PKCE | `core/secure_api_authcode.go` (`OAuthStart` / `OAuthCallback`) | The same steps, used today for outbound credentials. The sign-in flow follows the same shape with its own pending map. |
| Certificate sign-in | `core/clientcert` (`Generate`, `Assertion`) | Entra's certificate credential (private key JWT), already used for Teams and SecureAPI. The app proves itself without a client secret. |
| Waiting for approval | `AuthUser.Pending`, `AuthApproveUser` | A first-time SSO person can arrive as pending. |
| External base URL | `external_url` in `WebTable` | The fixed redirect URI. Never derived from the request's Host header. |
| No usable password | `AuthCheckPassword` refuses an empty `PassHash` (with the timing-equal dummy check) | An SSO-only account cannot be signed into by password. |

## The flow

1. **Login page.** When SSO is configured, the page shows a
   "Sign in with Microsoft" button above the password form. The label is
   a setting.
2. **`GET /login/sso`** mints `state`, `nonce` and a PKCE verifier. It
   keeps them server side keyed by `state`, with a 10-minute lifetime,
   the same way `oauthPending` does. It also sets a short-lived cookie
   (HttpOnly, SameSite=Lax, `/login/sso` path) carrying a hash of `state`.
   Then it redirects to the provider's authorize endpoint (from
   discovery) with `response_type=code`, `scope=openid profile email`,
   `response_mode=query`, the PKCE challenge and the nonce.
3. **`GET /login/sso/callback`**:
   - An `error` parameter from the provider ends here with its message on
     the login page.
   - The cookie must match `state`. This is the login CSRF check: without
     it, an attacker could send someone a callback link that signs them
     into the attacker's account, the same hole `LoginHandler`'s
     same-origin check closes for passwords. `response_mode=query` keeps
     it working, since a Lax cookie rides a top-level GET from the
     provider but not a cross-site POST (`form_post`).
   - The pending entry is taken out (single use) and its age checked.
   - The code is exchanged at the token endpoint with the PKCE verifier.
     The client authenticates with the certificate assertion, or with a
     client secret if that is what was configured.
   - The ID token is checked: signature (via `jwks.Verifier`), `iss`
     equal to `https://login.microsoftonline.com/<tenant-id>/v2.0`, `aud`
     equal to the client ID, `exp`/`nbf` with a small skew, `nonce`
     equal to the pending nonce, and `tid` equal to the configured tenant.
   - The access and refresh tokens are thrown away. Sign-in needs only
     the ID token; gohort does not call Microsoft Graph and keeps nothing
     that could act as the person at Microsoft.
4. **Who this is.** The identity key is the issuer plus the person's
   permanent ID (`oid` on Entra, `sub` on other providers). It is looked
   up in a new table of links (below). Found: that gohort user. Not
   found: the "new people" setting decides (below).
5. **Session.** The pending-account check from the password path applies.
   Then `AuthCreateSession`, the same cookie, the same lifetime. Activity
   records "signed in with Microsoft"; a refusal records `SignInFailed`
   with the reason, as a wrong password does today.

`/login/sso` and its callback join the list of auth pages that
`AuthMiddleware` always lets through.

## Linking an identity to a gohort user

**Never by email.** On Entra the `email` claim can be edited by users and
is not guaranteed verified. Matching on it is how several apps let one
tenant's user sign in as another's (the published "nOAuth" issue). The
link is made once, deliberately, and kept.

One new table, `auth_sso_links`, keyed by `<issuer>|<id>`, holding
`{username, linked_at, shown_as}`. `shown_as` is the
`preferred_username` at link time, for display only. `AuthUser` does not
change, so no records migrate. Deleting a user deletes their links.

Three ways a link is made:

- **The person links it themselves.** Signed in by password, they choose
  "Connect Microsoft sign-in" on their account page. That runs the same
  flow with a link mark on the pending entry. The callback links only if
  the signed-in user is the one who started it, which is
  `OAuthCallback`'s existing rule. This is how existing accounts,
  including the owner's, move over.
- **First sign-in creates the account** when the setting allows it. The
  new account's username is the `preferred_username` (the UPN, normally
  an email address), used as a name and never as a match. If an account
  with that name already exists, nothing is linked or created. The page
  says to sign in by password and connect, or to ask an administrator.
- **An administrator unlinks** from the user's row. Linking for someone
  else is left out: it would need the admin to know the person's object
  ID, and the self-link covers it.

## Settings (admin, a "Sign-in" section)

This is the instance's front door, so it is an administrator setting,
unlike integrations, which are per user.

| Setting | Values | Default |
|---|---|---|
| Microsoft sign-in | Off / On | Off |
| Tenant ID | A directory (tenant) GUID. `common`, `organizations` and `consumers` are refused in this phase. | (none) |
| Client ID | The app registration's application ID | (none) |
| Client credential | A certificate made here (the page shows it to upload to Entra, its thumbprint and when it expires), or a client secret (stored encrypted) | Certificate |
| Button label | Text | "Sign in with Microsoft" |
| New people | Refuse (link first) / Wait for approval / Let in | Wait for approval |
| Redirect URI | Read only: `<external_url>/login/sso/callback`, with a copy button, for the Entra registration | (derived) |

The page says what to do in Entra, in order: register an app (single
tenant), add the redirect URI under "Web", upload the certificate, copy
the tenant and client IDs here. If `external_url` is not set, the
section says that first and cannot be turned on.

"Password sign-in: Administrators only" keeps a way in when the tenant
or the registration breaks. When password sign-in is not On, the forgot
and reset pages refuse SSO-only accounts ("this account signs in with
Microsoft"). Otherwise a reset email would hand a password to an account
that is meant not to have one.

## Where the code goes

- **`core/oidc`**, a new package: discovery, the authorize URL, the code
  exchange, the ID token check. It speaks no core type, like `core/jwks`,
  so it costs core nothing against its export ceiling.
- The two handlers, the link table and the login button go beside
  `LoginHandler` in core's auth code, unexported.
- The settings section goes in `apps/admin`, beside "Public signup".
- The account page's "Connect Microsoft sign-in" goes on the existing
  account settings.

Keys for MCP, the Model API, the desktop bridge and peers are untouched:
they never use the login page.

## Testing

An in-process fake provider (`httptest`): discovery, a key set, a token
endpoint, tokens signed with `jwcrypt`. Nothing reaches Microsoft or the
host. Cases:

- A good sign-in lands a session for the linked user.
- Refused: wrong tenant (`tid`), wrong issuer, wrong audience, wrong or
  missing nonce, expired token, a key not in the set.
- A `state` used twice; a `state` older than 10 minutes; a callback
  without the browser's state cookie (the CSRF case).
- An unlinked identity: refused, pending or let in, per the setting. A
  name that already exists is neither linked nor created.
- A self-link completed by a different signed-in user links nothing.
- A pending account is refused like at the password form.
- An SSO-only account refuses a password, and refuses a reset when
  password sign-in is not On.

Then a live trial against the owner's own tenant.

## Steps

1. `core/oidc` and its fake-provider tests.
2. The two routes, the link table, the button, the activity lines.
3. The admin section: certificate made here, redirect URI shown, the
   Entra steps.
4. "Connect Microsoft sign-in" on the account page; unlink on the admin
   user row.
5. The live trial.

## Known limits of this phase

- **Turning someone off in Entra does not end a session already open.**
  Gohort learns of it at the next sign-in. A session lasts
  `sessionDuration()`. A shorter cap for SSO sessions is a later choice.
- **Sign-out is local.** The Microsoft session stays, so the button signs
  the person straight back in. Signing out of Microsoft too (RP-initiated
  logout) is later.
- **Groups do not map.** Entra groups to gohort app groups is a separate
  piece. Over about 200 groups, Entra sends a pointer instead of the
  list, which then needs a Graph call.
- **Entra may not send `email`.** It depends on the account and on
  optional claims. Nothing here depends on it: the link uses `oid`, and
  the new account's name uses `preferred_username`.

## Open questions for the owner

1. **The desktop app.** gohort-desktop runs its own loopback proxy and
   keeps the session cookie in its own jar (`gohort-desktop/proxy.go`).
   A redirect to Microsoft and back would land on the server directly,
   outside the proxy, so the cookie would end up in the wrong place. It
   is also unverified whether its webview navigates to an outside site
   in the window at all. Proposal: in this phase the desktop app keeps
   the password form. That means "Password sign-in: Off" is not offered
   until the desktop has its own SSO path. The likely path is a
   loopback redirect URI that the proxy catches and forwards, but that
   needs a trial first.
2. **Default for "New people".** Wait for approval is proposed. Let in is
   reasonable for a single tenant, where everyone who can sign in is
   already the organization's.
3. **Session length for SSO sign-ins.** The same as password (proposed),
   or shorter so that Entra offboarding takes effect sooner.

## As built

- **Code.** `core/oidc` (the protocol, the settings, the links) and
  `core/oidc/oidctest` (the fake provider every test signs in against). The
  routes are `core/auth_sso.go`, unexported beside `LoginHandler`, which
  shares their session cookie helper. Core's export count is unchanged, and
  its file ceiling went from 200 to 201 for that file.
- **Routes.** `GET /login/sso` and `GET /login/sso/callback` as above, plus
  three that need a session: `POST /api/sso/connect` (the Account page's
  Connect button), `GET /api/sso/me`, and `POST /api/sso/unlink` (the
  person's own links).
- **Admin.** One section, "Microsoft sign-in", on People after Sign-in: the
  settings, the certificate (made, downloaded, replaced), and a table of
  every connected identity with Disconnect. The table replaces unlinking
  from the user row.
- **Password sign-in stays on.** The "Password sign-in" setting (On /
  Administrators only / Off) is not built: with the desktop app still on the
  password form, Off cannot be offered, and Administrators only is the
  fallback for an Off that does not exist yet. It comes with the desktop's
  SSO path.
- **The open questions** took the proposals: the desktop keeps the password
  form, New people defaults to wait for approval, and an SSO session lasts
  as long as a password one.
- **A link to an account that no longer exists** signs nobody in and is
  dropped, and deleting an account drops its links, so a later account with
  the same name does not inherit them.

## Later

- Multi-tenant Entra: `organizations` with an allowlist of tenant IDs,
  and the issuer checked by pattern against `tid`.
- Other providers: the same code, a different issuer and label. More
  than one provider at once needs the button list and the settings to
  become a list.
- Group to app-group mapping.
- RP-initiated sign-out.
