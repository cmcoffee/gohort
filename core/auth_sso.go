package core

// Sign-in with Microsoft (OpenID Connect, docs/design/oidc-signin.md). The
// protocol and the stored settings and links are core/oidc's; what is here is
// the part that needs the session machinery beside LoginHandler: the routes,
// the pending sign-ins, which account an identity means, and the session at
// the end, made the same way a password sign-in makes one.
//
//	GET  /login/sso           start a sign-in
//	GET  /login/sso/callback  the provider sends the browser back here
//	POST /api/sso/connect     a signed-in person links their identity
//	GET  /api/sso/me          whether sign-in is on, and the person's links
//	POST /api/sso/unlink      the person removes one of their links

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/cmcoffee/gohort/core/accountname"
	"github.com/cmcoffee/gohort/core/activity"
	"github.com/cmcoffee/gohort/core/oidc"
)

const (
	ssoStartPath    = "/login/sso"
	ssoCallbackPath = "/login/sso/callback"
	// ssoCookie ties a sign-in to the browser that started it. Without it,
	// anyone could start a sign-in, stop at the provider's redirect, and send
	// the link to someone else, who would land signed in as the sender: the
	// login CSRF the password form's same-origin check closes.
	ssoCookie = "gohort_sso"
	// ssoPendingLife is how long a started sign-in waits for the provider.
	ssoPendingLife = 10 * time.Minute
)

// ssoIssuer is the provider's issuer for the settings: the Entra tenant's.
// A variable so a test can point sign-in at a provider of its own.
var ssoIssuer = func(s oidc.Settings) string { return oidc.EntraIssuer(s.Tenant) }

// ssoPending is a sign-in waiting for the provider.
type ssoPending struct {
	nonce    string
	verifier string
	browser  string // the hash of the browser's ssoCookie value
	link     string // the account linking its identity; "" for a sign-in
	at       time.Time
}

var (
	ssoPendingMu sync.Mutex
	ssoPendingBy = map[string]ssoPending{}
)

// ssoTake removes a pending sign-in, so a state is used once.
func ssoTake(state string) (ssoPending, bool) {
	ssoPendingMu.Lock()
	defer ssoPendingMu.Unlock()
	p, ok := ssoPendingBy[state]
	delete(ssoPendingBy, state)
	if !ok || time.Since(p.at) > ssoPendingLife {
		return ssoPending{}, false
	}
	return p, true
}

func ssoKeep(state string, p ssoPending) {
	ssoPendingMu.Lock()
	defer ssoPendingMu.Unlock()
	for k, old := range ssoPendingBy {
		if time.Since(old.at) > ssoPendingLife {
			delete(ssoPendingBy, k)
		}
	}
	ssoPendingBy[state] = p
}

func ssoHash(v string) string {
	sum := sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:])
}

// ssoRedirectURI is the callback the provider is registered with: the
// deployment's External URL, never the request's Host header, which the
// person sending it chooses.
func ssoRedirectURI() string {
	if WebBaseURL == nil {
		return ""
	}
	base := strings.TrimSuffix(strings.TrimSpace(WebBaseURL()), "/")
	if base == "" {
		return ""
	}
	return base + ssoCallbackPath
}

// ssoProvider is the configured provider, or why there is none.
func ssoProvider(db Database) (oidc.Settings, oidc.Provider, error) {
	s := oidc.LoadSettings(db)
	if !s.Enabled {
		return s, oidc.Provider{}, fmt.Errorf("%s is not turned on here", s.Label)
	}
	redirect := ssoRedirectURI()
	if redirect == "" {
		return s, oidc.Provider{}, fmt.Errorf("this deployment has no External URL set, so %s cannot send you back here", s.Label)
	}
	p, err := s.Provider(db, ssoIssuer(s), redirect)
	return s, p, err
}

// ssoButton is the login page's sign-in button, when it is on.
func ssoButton(db Database) string {
	if db == nil {
		return ""
	}
	s := oidc.LoadSettings(db)
	if !s.Enabled || ssoRedirectURI() == "" {
		return ""
	}
	return `    <form method="GET" action="` + ssoStartPath + `" class="sso-form">
      <button type="submit">` + html.EscapeString(s.Label) + `</button>
    </form>
    <div class="alt-link" style="text-align:center;margin:0.6rem 0">or with your password</div>
`
}

// ssoStartHandler begins a sign-in: GET /login/sso.
func ssoStartHandler(db Database) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		ssoBegin(w, r, db, "")
	}
}

// ssoBegin starts a sign-in, or a link for the account named, and sends the
// browser to the provider.
func ssoBegin(w http.ResponseWriter, r *http.Request, db Database, link string) {
	_, p, err := ssoProvider(db)
	if err != nil {
		serveLoginPage(w, err.Error())
		return
	}
	req, err := p.Start(r.Context())
	if err != nil {
		Log("[auth] Microsoft sign-in could not start: %v", err)
		serveLoginPage(w, "Microsoft sign-in could not start: "+err.Error())
		return
	}
	browser := generateToken()
	ssoKeep(req.State, ssoPending{nonce: req.Nonce, verifier: req.Verifier, browser: ssoHash(browser), link: link, at: time.Now()})
	http.SetCookie(w, &http.Cookie{
		Name: ssoCookie, Value: browser, Path: ssoStartPath, HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Secure: requestIsHTTPS(r), MaxAge: int(ssoPendingLife.Seconds()),
	})
	http.Redirect(w, r, req.URL, http.StatusSeeOther)
}

// ssoCallbackHandler finishes a sign-in or a link: GET /login/sso/callback.
func ssoCallbackHandler(db Database) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		q := r.URL.Query()
		http.SetCookie(w, &http.Cookie{Name: ssoCookie, Value: "", Path: ssoStartPath, HttpOnly: true, MaxAge: -1})
		pending, ok := ssoTake(q.Get("state"))
		if e := q.Get("error"); e != "" {
			desc := strings.TrimSpace(strings.SplitN(q.Get("error_description"), "\n", 2)[0])
			Log("[auth] Microsoft sign-in came back with %s: %s", e, desc)
			RecordActivity(r, activity.SignInFailed, "", "Microsoft sign-in refused at Microsoft: "+e)
			serveLoginPage(w, "Microsoft did not sign you in: "+firstNonEmpty(desc, e))
			return
		}
		if !ok {
			serveLoginPage(w, "That sign-in has expired or was already used. Start it again.")
			return
		}
		cookie, err := r.Cookie(ssoCookie)
		if err != nil || subtle.ConstantTimeCompare([]byte(ssoHash(cookie.Value)), []byte(pending.browser)) != 1 {
			Log("[auth] refused a Microsoft sign-in finished in a browser that did not start it, from %s", ClientIP(r))
			RecordActivity(r, activity.SignInFailed, "", "Microsoft sign-in refused: finished in a browser that did not start it")
			serveLoginPage(w, "That sign-in was started in another browser. Start it again here.")
			return
		}
		s, p, err := ssoProvider(db)
		if err != nil {
			serveLoginPage(w, err.Error())
			return
		}
		id, err := p.Finish(r.Context(), q.Get("code"), pending.verifier, pending.nonce)
		if err != nil {
			Log("[auth] Microsoft sign-in refused: %v", err)
			RecordActivity(r, activity.SignInFailed, "", "Microsoft sign-in refused: "+err.Error())
			serveLoginPage(w, "Microsoft sign-in did not complete: "+err.Error())
			return
		}
		if pending.link != "" {
			ssoFinishLink(w, r, db, pending.link, id)
			return
		}
		ssoFinishSignIn(w, r, db, s, id)
	}
}

// ssoFinishLink ties the identity to the account that started the link, if
// that account is still the one signed in here.
func ssoFinishLink(w http.ResponseWriter, r *http.Request, db Database, account string, id oidc.Identity) {
	if AuthCurrentUser(r) != account {
		serveLoginPage(w, "Connecting Microsoft sign-in was started by a different account. Sign in as yourself and start it again.")
		return
	}
	if l, ok := oidc.LinkedAccount(db, id); ok {
		if l.Username == account {
			http.Redirect(w, r, "/account/", http.StatusFound)
			return
		}
		Log("[auth] %q tried to connect %s, which is connected to another account", account, id.Shown())
		RecordActivity(r, activity.Account, account, "refused: "+id.Shown()+" is connected to another account")
		serveLoginPage(w, id.Shown()+" is already connected to another account here. An administrator can disconnect it.")
		return
	}
	oidc.SetLink(db, id, account, time.Now())
	Log("[auth] %q connected Microsoft sign-in %s", account, id.Shown())
	RecordActivity(r, activity.Account, account, "connected Microsoft sign-in "+id.Shown())
	http.Redirect(w, r, "/account/", http.StatusFound)
}

// ssoFinishSignIn signs in the account the identity is linked to, or makes
// one when the settings say to.
func ssoFinishSignIn(w http.ResponseWriter, r *http.Request, db Database, s oidc.Settings, id oidc.Identity) {
	username := ""
	if l, ok := oidc.LinkedAccount(db, id); ok {
		if _, exists := AuthGetUser(db, l.Username); exists {
			username = l.Username
		} else {
			oidc.Unlink(db, l.Username, id.Key())
		}
	}
	if username == "" {
		made, msg := ssoNewAccount(r, db, s, id)
		if made == "" {
			serveLoginPage(w, msg)
			return
		}
		username = made
	}
	if user, ok := AuthGetUser(db, username); ok && user.Pending {
		RecordActivity(r, activity.SignInFailed, username, "refused: the account is waiting for approval")
		serveLoginPage(w, "Your account is pending approval by an administrator.")
		return
	}
	setSessionCookie(w, r, AuthCreateSession(db, username))
	Log("[auth] user %q signed in with Microsoft (%s) from %s", username, id.Shown(), ClientIP(r))
	RecordActivity(r, activity.SignIn, username, "signed in with Microsoft ("+id.Shown()+")")
	http.Redirect(w, r, "/", http.StatusFound)
}

// ssoNewAccount makes an account for an identity no account is linked to,
// when the settings say to: named for the identity's username, never matched
// to an existing account by it. It returns the account, or why there is none.
func ssoNewAccount(r *http.Request, db Database, s oidc.Settings, id oidc.Identity) (string, string) {
	connect := "Sign in with your password and connect Microsoft sign-in from your account page, or ask an administrator."
	if s.NewPeople == oidc.NewPeopleRefuse {
		RecordActivity(r, activity.SignInFailed, id.Shown(), "Microsoft sign-in refused: no account is connected to it")
		return "", "No account here is connected to " + id.Shown() + ". " + connect
	}
	name, err := accountname.Canonical(firstNonEmpty(id.PreferredUsername, id.Email))
	if err != nil {
		return "", "Microsoft did not say a username for " + id.Shown() + " that can name an account here. " + connect
	}
	name, err = authNewName(db, name)
	if err != nil {
		RecordActivity(r, activity.SignInFailed, id.Shown(), "Microsoft sign-in refused: an account with that name exists and is not connected")
		return "", "An account named " + id.Shown() + " already exists here and is not connected to Microsoft sign-in. " + connect
	}
	// No password: it signs in with Microsoft, and AuthCheckPassword refuses
	// an account with none.
	db.Set(AuthTable, "user:"+name, AuthUser{Username: name, Pending: s.NewPeople == oidc.NewPeopleApproval})
	oidc.SetLink(db, id, name, time.Now())
	if s.NewPeople == oidc.NewPeopleApproval {
		Log("[auth] new account from Microsoft sign-in (pending): %q from %s", name, ClientIP(r))
		RecordActivity(r, activity.SignUp, name, "signed up with Microsoft; waiting for approval")
		NotifyAdmin("["+ServiceName()+"] New sign-in pending approval",
			fmt.Sprintf("Someone signed in with Microsoft on %s and is awaiting approval.\n\nAccount: %s\n\nAdmin panel: %s/admin/\n", DashboardURL(), name, DashboardURL()))
	} else {
		Log("[auth] new account from Microsoft sign-in: %q from %s", name, ClientIP(r))
		RecordActivity(r, activity.SignUp, name, "signed up with Microsoft")
	}
	return name, ""
}

// ssoConnectHandler starts linking the signed-in person's identity:
// POST /api/sso/connect, from their account page.
func ssoConnectHandler(db Database) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !SameOriginRequest(r) {
			http.Error(w, "cross-origin request rejected", http.StatusForbidden)
			return
		}
		user := AuthCurrentUser(r)
		if user == "" {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		ssoBegin(w, r, db, user)
	}
}

// ssoLinkRow is one link as the account page shows it.
type ssoLinkRow struct {
	Key      string    `json:"key"`
	ShownAs  string    `json:"shown_as"`
	LinkedAt time.Time `json:"linked_at"`
}

// ssoMeHandler is whether sign-in is on, and the person's links:
// GET /api/sso/me.
func ssoMeHandler(db Database) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user := AuthCurrentUser(r)
		if user == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		s := oidc.LoadSettings(db)
		rows := []ssoLinkRow{}
		for _, l := range oidc.LinksFor(db, user) {
			rows = append(rows, ssoLinkRow{Key: l.Key, ShownAs: l.ShownAs, LinkedAt: l.LinkedAt})
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"available": s.Enabled && ssoRedirectURI() != "",
			"label":     s.Label,
			"links":     rows,
		})
	}
}

// ssoUnlinkHandler removes one of the person's own links:
// POST /api/sso/unlink {"key": ...}.
func ssoUnlinkHandler(db Database) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		user := AuthCurrentUser(r)
		if user == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var body struct {
			Key string `json:"key"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || body.Key == "" {
			http.Error(w, "say which connection to remove", http.StatusBadRequest)
			return
		}
		var shown string
		for _, l := range oidc.LinksFor(db, user) {
			if l.Key == body.Key {
				shown = l.ShownAs
			}
		}
		if !oidc.Unlink(db, user, body.Key) {
			http.Error(w, "no such connection on your account", http.StatusNotFound)
			return
		}
		Log("[auth] %q disconnected Microsoft sign-in %s", user, shown)
		RecordActivity(r, activity.Account, user, "disconnected Microsoft sign-in "+shown)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"message": "Disconnected " + shown + "."})
	}
}

// setSessionCookie is the session cookie a sign-in ends with, password or
// Microsoft alike.
func setSessionCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     auth_cookie_name,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   requestIsHTTPS(r),
		MaxAge:   sessionMaxAge(),
	})
}
