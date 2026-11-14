package admin

// Microsoft sign-in (docs/design/oidc-signin.md): the deployment's Entra app
// registration, the certificate it signs in with, what happens to someone new,
// and the identities connected to accounts here. The settings and links are
// core/oidc's; the sign-in itself is core's (auth_sso.go).

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/activity"
	"github.com/cmcoffee/gohort/core/clientcert"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/oidc"
	"github.com/cmcoffee/gohort/core/ui"
)

// secretPlaceholder is what the form shows for a client secret that is set:
// the secret itself never leaves the server.
const secretPlaceholder = "(configured)"

// ssoCertLife is how long a certificate made here lasts.
const ssoCertLife = 2 * 365 * 24 * time.Hour

func (a *AdminApp) ssoSections() []ui.Section {
	return []ui.Section{{
		Title:    "Microsoft sign-in",
		Subtitle: "Let people sign in with their work Microsoft account (one Entra directory). Saved automatically as you edit.",
		Detail: "In Microsoft Entra (portal.azure.com, App registrations), in this order:\n\n" +
			"1. New registration: any name, \"Accounts in this organizational directory only\" (single tenant), redirect URI type Web, the Redirect URI shown below.\n" +
			"2. Certificates & secrets: upload the certificate made here (Make a certificate, then Download), or make a client secret and paste it below.\n" +
			"3. Copy the Directory (tenant) ID and the Application (client) ID from the registration's Overview into the fields below, then turn sign-in on.\n\n" +
			"Sign-in reads only who the person is (openid, profile, email); gohort keeps no Microsoft token. An identity is tied to an account once, never matched by email: an existing account connects it from its own Account page, and someone new is handled as New people says. " +
			"Password sign-in stays on. A person turned off in Entra keeps a session already open here until it ends.",
		Body: ui.Stack{Children: []ui.Component{
			ui.FormPanel{
				Source: "api/sso",
				Method: "PATCH",
				Fields: []ui.FormField{
					{Field: "redirect_uri", Label: "Redirect URI", Type: "readonly", Copy: true,
						Help: "Register this in Entra exactly, as a Web redirect URI."},
					{Field: "tenant", Label: "Directory (tenant) ID", Type: "text", SingleLine: true,
						Placeholder: "the directory's GUID", Help: "From the app registration's Overview. Only people in this directory can sign in."},
					{Field: "client_id", Label: "Application (client) ID", Type: "text", SingleLine: true,
						Placeholder: "the registration's GUID", Help: "From the same Overview page."},
					{Field: "credential", Label: "Proves itself with", Type: "select", Options: []ui.SelectOption{
						{Value: oidc.CredentialCertificate, Label: "A certificate made here (recommended)"},
						{Value: oidc.CredentialSecret, Label: "A client secret from Entra"},
					}, Help: "A certificate's private key never leaves this server; a secret is a password Entra issues."},
					{Field: "certificate", Label: "Certificate", Type: "readonly", ShowWhen: "credential:certificate",
						Help: "Upload its public part to the registration's Certificates & secrets."},
					{Field: "cert_download", Label: "Public certificate", Type: "link", Placeholder: "Download the public certificate",
						ShowWhen: "credential:certificate"},
					{Field: "client_secret", Label: "Client secret", Type: "password", SingleLine: true, ShowWhen: "credential:secret",
						Help: "The secret's Value from Entra, not its ID. Kept encrypted."},
					{Field: "label", Label: "Button text", Type: "text", Placeholder: oidc.DefaultLabel,
						Help: "What the login page's button says."},
					{Field: "new_people", Label: "New people", Type: "select", Options: []ui.SelectOption{
						{Value: oidc.NewPeopleApproval, Label: "Make an account that waits for approval"},
						{Value: oidc.NewPeopleLetIn, Label: "Make an account and let them in"},
						{Value: oidc.NewPeopleRefuse, Label: "Refuse: they connect an existing account first"},
					}, Help: "Someone whose Microsoft identity no account here is connected to.",
						Detail: "A new account is named for their Microsoft username. If an account with that name exists and is not connected, nothing is made or connected: they sign in with its password and connect from their Account page."},
					{Field: "enabled", Label: "Microsoft sign-in", Type: "toggle",
						Help: "On, the login page offers the button. It turns on only once the fields above can sign someone in."},
					{Field: "status", Label: "Status", Type: "readonly"},
				},
			},
			ui.ActionList{
				Source:     "api/sso/actions",
				PostTo:     "api/sso/certificate",
				ReloadSelf: true,
				Invalidate: []string{"api/sso"},
			},
			ui.Table{
				Source:    "api/sso/links",
				RowKey:    "key",
				EmptyText: "No Microsoft identity is connected to an account yet.",
				Columns: []ui.Col{
					{Field: "account", Label: "Account", Flex: 2},
					{Field: "shown_as", Label: "Microsoft identity", Flex: 2},
					{Field: "linked_at", Label: "Connected", Format: "reltime", Mute: true},
				},
				RowActions: []ui.RowAction{{Type: "button", Label: "Disconnect", Method: "DELETE",
					PostTo: "api/sso/links?key={key}", Confirm: "Disconnect this Microsoft identity? It will no longer sign in to the account."}},
			},
		}},
	}}
}

func (a *AdminApp) registerSSORoutes(sub *http.ServeMux) {
	sub.HandleFunc("/api/sso", jsonapi.Methods{
		"GET":   jsonapi.Get(a.db, a.handleSSOGet, a.gate()),
		"PATCH": jsonapi.Patch(a.db, a.handleSSOSave, a.gate()),
	}.Serve)
	sub.HandleFunc("/api/sso/actions", jsonapi.Get(a.db, a.handleSSOActions, a.gate()))
	sub.HandleFunc("/api/sso/certificate", jsonapi.Methods{
		"POST": jsonapi.Post(a.db, a.handleSSOMakeCertificate, a.gate()),
	}.Serve)
	sub.HandleFunc("/api/sso/certificate.cer", a.handleSSOCertificateDownload)
	sub.HandleFunc("/api/sso/links", jsonapi.Methods{
		"GET":    jsonapi.Get(a.db, a.handleSSOLinks, a.gate()),
		"DELETE": jsonapi.Delete(a.db, a.handleSSOUnlink, a.gate()),
	}.Serve)
}

// ssoRedirectURI is the callback Entra is registered with, or "" with no
// External URL.
func ssoRedirectURI() string {
	if WebBaseURL == nil {
		return ""
	}
	base := strings.TrimSuffix(strings.TrimSpace(WebBaseURL()), "/")
	if base == "" {
		return ""
	}
	return base + "/login/sso/callback"
}

func (a *AdminApp) handleSSOGet(c *jsonapi.Call) (any, error) {
	s := oidc.LoadSettings(a.db)
	redirect := ssoRedirectURI()
	out := map[string]any{
		"enabled": s.Enabled, "tenant": s.Tenant, "client_id": s.ClientID, "credential": s.Credential,
		"label": s.Label, "new_people": s.NewPeople,
		"redirect_uri":  redirect,
		"client_secret": "",
		"certificate":   "None yet: make one below, then download it.",
		"cert_download": "",
	}
	if redirect == "" {
		out["redirect_uri"] = "Set External URL first (Overview > Site): Entra sends people back to it."
	}
	if oidc.LoadSecret(a.db) != "" {
		out["client_secret"] = secretPlaceholder
	}
	if cert, ok := oidc.LoadCertificate(a.db); ok {
		out["certificate"] = "Thumbprint " + cert.ThumbprintSHA1() + ", expires " + cert.Expires().Format("Jan 2, 2006")
		out["cert_download"] = "/admin/api/sso/certificate.cer"
	}
	out["status"] = ssoStatus(a.db, s, redirect)
	return out, nil
}

// ssoStatus says what the settings amount to.
func ssoStatus(db Database, s oidc.Settings, redirect string) string {
	err := oidc.Ready(db, s)
	switch {
	case s.Enabled && redirect == "":
		return "On, but the login page shows no button: set External URL (Overview > Site)."
	case s.Enabled && err != nil:
		return "On, but it cannot sign anyone in: " + err.Error() + "."
	case s.Enabled:
		return "On: the login page offers \"" + s.Label + "\"."
	case err != nil:
		return "Off. To turn it on: " + err.Error() + "."
	}
	return "Off, and ready to turn on."
}

func (a *AdminApp) handleSSOSave(c *jsonapi.Call, req struct {
	Enabled      *bool   `json:"enabled"`
	Tenant       *string `json:"tenant"`
	ClientID     *string `json:"client_id"`
	Credential   *string `json:"credential"`
	ClientSecret *string `json:"client_secret"`
	Label        *string `json:"label"`
	NewPeople    *string `json:"new_people"`
}) (any, error) {
	s := oidc.LoadSettings(a.db)
	before := s
	if req.Tenant != nil {
		s.Tenant = strings.TrimSpace(*req.Tenant)
	}
	if req.ClientID != nil {
		s.ClientID = strings.TrimSpace(*req.ClientID)
	}
	if req.Credential != nil {
		switch *req.Credential {
		case oidc.CredentialCertificate, oidc.CredentialSecret:
			s.Credential = *req.Credential
		default:
			return nil, jsonapi.BadRequest("credential is certificate or secret")
		}
	}
	if req.Label != nil {
		s.Label = strings.TrimSpace(*req.Label)
	}
	if req.NewPeople != nil {
		switch *req.NewPeople {
		case oidc.NewPeopleApproval, oidc.NewPeopleLetIn, oidc.NewPeopleRefuse:
			s.NewPeople = *req.NewPeople
		default:
			return nil, jsonapi.BadRequest("new_people is approval, let_in or refuse")
		}
	}
	if req.Enabled != nil {
		s.Enabled = *req.Enabled
	}
	// The secret is saved first, so a form that sets it and turns sign-in on
	// in one save can turn on. The placeholder is what the form shows for a
	// secret that is set, and saving it back changes nothing.
	secretChanged := false
	if req.ClientSecret != nil && *req.ClientSecret != secretPlaceholder {
		if strings.TrimSpace(*req.ClientSecret) != oidc.LoadSecret(a.db) {
			oidc.SaveSecret(a.db, *req.ClientSecret)
			secretChanged = true
		}
	}
	if err := oidc.SaveSettings(a.db, s); err != nil {
		if secretChanged {
			adminChange(c.R, activity.Settings, "changed the Microsoft sign-in client secret")
		}
		return nil, jsonapi.BadRequest(err.Error())
	}
	if secretChanged {
		adminChange(c.R, activity.Settings, "changed the Microsoft sign-in client secret")
	}
	after := oidc.LoadSettings(a.db)
	for _, ch := range []struct{ name, was, is string }{
		{"tenant", before.Tenant, after.Tenant},
		{"client ID", before.ClientID, after.ClientID},
		{"credential", before.Credential, after.Credential},
		{"button text", before.Label, after.Label},
		{"new people", before.NewPeople, after.NewPeople},
	} {
		if ch.was != ch.is {
			adminChange(c.R, activity.Settings, "set Microsoft sign-in %s to %q", ch.name, ch.is)
		}
	}
	if before.Enabled != after.Enabled {
		word := "off"
		if after.Enabled {
			word = "on"
		}
		adminChange(c.R, activity.Settings, "turned Microsoft sign-in %s", word)
	}
	return map[string]string{"status": "updated"}, nil
}

func (a *AdminApp) handleSSOActions(c *jsonapi.Call) (any, error) {
	row := map[string]string{
		"Label": "Make a certificate", "button": "Make",
		"Desc": "A new key and certificate for this registration, lasting two years. Download it from the form above and upload it to Entra.",
	}
	if cert, ok := oidc.LoadCertificate(a.db); ok {
		row["Label"] = "Make a new certificate"
		row["Desc"] = "Replaces the one expiring " + cert.Expires().Format("Jan 2, 2006") + ". Upload the new one to Entra; until then Microsoft sign-in fails."
		row["confirm"] = "Replace the certificate? Microsoft sign-in fails until the new one is uploaded to Entra."
	}
	return []map[string]string{row}, nil
}

func (a *AdminApp) handleSSOMakeCertificate(c *jsonapi.Call, _ struct{}) (any, error) {
	cert, err := clientcert.Generate("gohort sign-in ("+ServiceName()+")", ssoCertLife)
	if err != nil {
		return nil, err
	}
	oidc.SaveCertificate(a.db, cert)
	adminChange(c.R, activity.Credential, "made a Microsoft sign-in certificate (thumbprint %s)", cert.ThumbprintSHA1())
	return map[string]string{"message": "Made a certificate, thumbprint " + cert.ThumbprintSHA1() + ". Download it from the form and upload it to the app registration in Entra."}, nil
}

// handleSSOCertificateDownload sends the public certificate, the part Entra is
// given. It holds no secret.
func (a *AdminApp) handleSSOCertificateDownload(w http.ResponseWriter, r *http.Request) {
	if !a.requireAdmin(w, r) {
		return
	}
	cert, ok := oidc.LoadCertificate(a.db)
	if !ok {
		http.Error(w, "no certificate has been made", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/x-pem-file")
	w.Header().Set("Content-Disposition", `attachment; filename="gohort-sign-in.cer"`)
	w.Write([]byte(cert.CertificatePEM()))
}

type ssoLinkRow struct {
	Key      string    `json:"key"`
	Account  string    `json:"account"`
	ShownAs  string    `json:"shown_as"`
	LinkedAt time.Time `json:"linked_at"`
}

func (a *AdminApp) handleSSOLinks(c *jsonapi.Call) (any, error) {
	rows := []ssoLinkRow{}
	for _, k := range a.db.Keys(oidc.LinksTable) {
		var l oidc.Link
		if a.db.Get(oidc.LinksTable, k, &l) {
			rows = append(rows, ssoLinkRow{Key: k, Account: l.Username, ShownAs: l.ShownAs, LinkedAt: l.LinkedAt})
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].LinkedAt.After(rows[j].LinkedAt) })
	return rows, nil
}

func (a *AdminApp) handleSSOUnlink(c *jsonapi.Call, _ struct{}) (any, error) {
	key := c.R.URL.Query().Get("key")
	var l oidc.Link
	if key == "" || !a.db.Get(oidc.LinksTable, key, &l) {
		return nil, jsonapi.NotFound("no such connection")
	}
	oidc.Unlink(a.db, l.Username, key)
	adminChange(c.R, activity.Account, "disconnected Microsoft sign-in %s from %s", l.ShownAs, l.Username)
	return map[string]string{"message": fmt.Sprintf("Disconnected %s from %s.", l.ShownAs, l.Username)}, nil
}
