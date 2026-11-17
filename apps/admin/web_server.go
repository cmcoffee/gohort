package admin

// Web server: the [web] settings of gohort.ini (core/serverconf), on the
// Overview tab under Site. They were reachable only from the terminal setup
// menu; the page writes the same keys, so the two never disagree.
//
// Two of them apply at once, because they are read on every request: who may
// reach the admin pages, and which reverse proxies are believed. The others
// are read when the server starts, and the page says which saved changes are
// waiting for a restart until one happens.
//
// A save that would lock its maker out is refused: an admin address list that
// leaves out the address this request comes from, judged with the proxy list
// being saved, since a proxy list change moves what that address is.

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/cmcoffee/gohort/core/activity"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/netgate"
	"github.com/cmcoffee/gohort/core/serverconf"
	"github.com/cmcoffee/gohort/core/ui"
)

const webServerSource = "api/web-server"

func (a *AdminApp) registerWebServerRoutes(sub *http.ServeMux) {
	sub.HandleFunc("/"+webServerSource, jsonapi.Methods{
		"GET":  jsonapi.Get(a.db, a.handleWebServer, a.gate()),
		"POST": jsonapi.Post(a.db, a.handleWebServerSave, a.gate()),
	}.Serve)
}

// webServerForm is the settings as the page shows them, with what is running
// and what waits for a restart.
type webServerForm struct {
	serverconf.Settings
	Running string `json:"running"`
	Waiting string `json:"waiting"`
}

func webServerView(s serverconf.Settings) webServerForm {
	run := serverconf.Running.Normalize()
	scheme := "HTTP"
	if run.TLSCert != "" || run.TLSSelfSigned {
		scheme = "HTTPS"
	}
	running := fmt.Sprintf("%s on %s, %d app(s) at once", scheme, run.Addr, run.MaxConcurrent)
	if serverconf.AddrGiven {
		running += ". The address was given on the command line, so a saved one does not change it"
	}
	waiting := "Nothing: what is saved is what is running."
	if w := serverconf.WaitingForRestart(s); len(w) > 0 {
		waiting = "Saved, and waiting for a restart: " + strings.Join(w, ", ") + "."
	}
	return webServerForm{Settings: s, Running: running, Waiting: waiting}
}

func (a *AdminApp) handleWebServer(c *jsonapi.Call) (webServerForm, error) {
	if serverconf.Load == nil {
		return webServerForm{}, jsonapi.Status(http.StatusServiceUnavailable, "this build cannot change the web server's settings from here: use gohort --setup")
	}
	return webServerView(serverconf.Load()), nil
}

func (a *AdminApp) handleWebServerSave(c *jsonapi.Call, f webServerForm) (webServerForm, error) {
	if serverconf.Load == nil || serverconf.Save == nil {
		return f, jsonapi.Status(http.StatusServiceUnavailable, "this build cannot change the web server's settings from here: use gohort --setup")
	}
	s := f.Settings.Normalize()
	if err := serverconf.Check(s); err != nil {
		return f, jsonapi.BadRequest(err.Error())
	}
	// Judged as the next request will be: with the proxy list being saved.
	if ip := netgate.ClientIPTrusting(c.R, s.TrustedProxies); !netgate.AddressListAllows(s.AdminAllowedIPs, ip) {
		where := "an address it cannot tell"
		if ip != nil {
			where = ip.String()
		}
		return f, jsonapi.BadRequest(fmt.Sprintf("this would lock you out: you are reaching this page from %s, which the admin addresses leave out. Add it, or a range that holds it", where))
	}
	if err := serverconf.Save(s); err != nil {
		return f, err
	}
	adminChange(c.R, activity.Settings, "saved the web server settings (address %q, HTTPS %s, %d app(s) at once, admin addresses %q, trusted proxies %q)",
		s.Addr, httpsWords(s), s.MaxConcurrent, s.AdminAllowedIPs, s.TrustedProxies)
	return webServerView(serverconf.Load()), nil
}

// httpsWords says how s serves HTTPS, for the activity log.
func httpsWords(s serverconf.Settings) string {
	switch {
	case s.TLSCert != "":
		return "from " + s.TLSCert
	case s.TLSSelfSigned:
		return "self-signed"
	}
	return "off"
}

// webServerSection is the page's section, on the Overview tab after Site.
func webServerSection() ui.Section {
	return ui.Section{
		Title:    "Web server",
		Subtitle: "Where gohort listens, HTTPS, and who may reach the admin pages. The address, HTTPS and the app limit apply when the service restarts; the two address lists apply at once.",
		Detail: "These are the [web] settings in gohort.ini, the same ones gohort --setup edits.\n\n" +
			"HTTPS: give a certificate and its key (PEM files on this machine), or choose a self-signed certificate, which browsers warn about. The files are checked before saving, so a certificate that does not load cannot stop the server coming back after its restart. Behind a reverse proxy that handles HTTPS, leave both off and list the proxy under Trusted proxies.\n\n" +
			"Trusted proxies: reverse proxies whose forwarding headers (X-Forwarded-For, X-Forwarded-Proto) are believed, besides this machine itself. A proxy on another machine left off this list makes every visitor look like the proxy: one address for the login lockout and the admin addresses, and the sign-in cookie loses its HTTPS-only flag.\n\n" +
			"Admin addresses: when set, the admin pages answer only these addresses or ranges. A save that leaves out the address you are on now is refused, so it cannot lock you out.",
		Body: ui.FormPanel{
			Source:      webServerSource,
			Method:      "POST",
			SubmitLabel: "Save",
			Fields: []ui.FormField{
				{Field: "running", Label: "Running now", Type: "readonly"},
				{Field: "waiting", Label: "Waiting for a restart", Type: "readonly"},
				{Field: "addr", Label: "Address", Type: "text", Placeholder: "host:port",
					Help: "Host and port to listen on; :8080 is every interface. Applies on restart."},
				{Field: "tls_self_signed", Label: "Self-signed HTTPS", Type: "toggle",
					Help: "HTTPS with a certificate gohort makes itself. Browsers warn about it. Applies on restart."},
				{Field: "tls_cert", Label: "Certificate file", Type: "text", ShowWhen: "!tls_self_signed",
					Placeholder: "path to the certificate file",
					Help:        "Path to the certificate (PEM) on this machine, with its key below. Leave both blank for plain HTTP. Applies on restart."},
				{Field: "tls_key", Label: "Key file", Type: "text", ShowWhen: "!tls_self_signed",
					Placeholder: "path to the key file",
					Help:        "Path to the certificate's private key (PEM)."},
				{Field: "max_concurrent", Label: "Apps at once", Type: "number", Min: 1, Max: 64,
					Help: "How many apps may run a job at the same time; the rest wait their turn. Applies on restart."},
				{Field: "trusted_proxies", Label: "Trusted proxies", Type: "text",
					Placeholder: "addresses or ranges",
					Help:        "Reverse proxies whose forwarding headers are believed, as addresses or ranges, comma-separated. This machine is always trusted. Applies at once."},
				{Field: "admin_allowed_ips", Label: "Admin addresses", Type: "text",
					Placeholder: "addresses or ranges; blank for any",
					Help:        "Addresses or ranges allowed to reach the admin pages, comma-separated. Blank allows any signed-in administrator. Applies at once."},
			},
		},
	}
}
