package admin

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

// registerSystemRoutes wires the system API under the admin sub-mux.
func (a *AdminApp) registerSystemRoutes(sub *http.ServeMux) {
	// API: list available apps.
	sub.HandleFunc("/api/apps", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		a.handleListApps(w, r)
	})

	// API: system status.
	sub.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		a.handleStatus(w, r)
	})

	// System Dependencies — probes optional host binaries (ffmpeg, pdftotext,
	// bwrap, …) so the admin panel can show what's installed and what each gates.
	sub.HandleFunc("/api/dependencies", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(CheckDependencies())
	})

	sub.HandleFunc("/api/settings", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		switch r.Method {
		case http.MethodGet:
			a.handleGetSettings(w, r)
		case http.MethodPut, http.MethodPost:
			// FormPanel auto-save defaults to POST; accept both so an
			// in-place edit form saves without a per-field Method override.
			a.handleUpdateSettings(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// API: revert retrieval/limit tunables to their code defaults.
	sub.HandleFunc("/api/settings/reset-tunables", func(w http.ResponseWriter, r *http.Request) {
		if !a.requireAdmin(w, r) {
			return
		}
		a.handleResetTunables(w, r)
	})

}

func (a *AdminApp) handleStatus(w http.ResponseWriter, r *http.Request) {
	var allow_signup bool
	a.db.Get(WebTable, "allow_signup", &allow_signup)
	sandbox := GetSandboxStatus()
	status := map[string]interface{}{
		// Surfaced because a one-time log line at first use is not somewhere
		// anyone looks, and "every shell command on this host runs at the
		// daemon's privilege" is not a fact an operator should have to
		// discover from scrollback — least of all on macOS, where it is the
		// permanent state and the old warning's advice was impossible.
		"sandbox_backend":  sandbox.Backend,
		"sandbox_confined": sandbox.Confined,
		// The severity the panel colours by. Decided here because only this
		// side knows that false is the bad direction — and a row reading
		// "confined: false" in the same grey as "user count: 3" is the row a
		// reader most needs to notice looking exactly like one they do not.
		"sandbox_status":   map[bool]string{true: "ok", false: "bad"}[sandbox.Confined],
		"sandbox_required": sandbox.Required,
		// Refusing is the state that changed meaning when confinement became
		// the default: an unconfined host no longer runs shell tools anyway,
		// it refuses them. Without this the panel says "confined: false" and
		// an operator reasonably concludes their tools are running wide open,
		// when in fact they are not running at all — opposite diagnoses, and
		// opposite fixes.
		"sandbox_refusing": sandbox.Refusing,
		// Which of the three bypass settings is live. "required: false" alone
		// cannot distinguish "everyone may run unconfined" from "admins may",
		// and those are very different deployments.
		"sandbox_bypass": sandbox.Bypass,
		// What a confined command may CONSUME. "Confined: true" reads as a
		// complete answer and is not one — a command inside a perfect mount
		// namespace can still fill the disk or pin a core — so the ceiling
		// gets its own row rather than living only in an env var nobody can
		// see from here.
		"sandbox_limits":  sandbox.LimitSummary,
		"sandbox_advice":  sandbox.Advice,
		"tls_enabled":     TLSEnabled(),
		"tls_self_signed": TLSSelfSignedEnabled(),
		"auth_enabled":    AuthHasUsers(a.db),
		"user_count":      len(AuthListUsers(a.db)),
		"active_sessions": len(AllLiveSessions()),
		"allow_signup":    allow_signup,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

func (a *AdminApp) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	var allow_signup, ollama_proxy_enabled bool
	var session_days, session_absolute_days, max_attempts, lockout_minutes, ollama_proxy_port, fetch_cache_quota_mb int
	var service_name, external_url, notify_from string
	a.db.Get(WebTable, "allow_signup", &allow_signup)
	a.db.Get(WebTable, "session_days", &session_days)
	// Presence, not value: 0 is a legal setting here (no ceiling), so an
	// absent key has to be told apart from an operator who chose zero.
	session_absolute_set := a.db.Get(WebTable, "session_absolute_days", &session_absolute_days)
	a.db.Get(WebTable, "max_login_attempts", &max_attempts)
	a.db.Get(WebTable, "lockout_minutes", &lockout_minutes)
	a.db.Get(WebTable, "service_name", &service_name)
	a.db.Get(WebTable, "external_url", &external_url)
	a.db.Get(WebTable, "notify_from", &notify_from)
	a.db.Get(WebTable, "ollama_proxy_enabled", &ollama_proxy_enabled)
	a.db.Get(WebTable, "ollama_proxy_port", &ollama_proxy_port)
	var ollama_proxy_bind string
	a.db.Get(WebTable, "ollama_proxy_bind", &ollama_proxy_bind)
	if ollama_proxy_bind == "" {
		ollama_proxy_bind = "127.0.0.1" // matches the proxy's own default
	}
	a.db.Get(WebTable, "fetch_cache_quota_mb", &fetch_cache_quota_mb)
	if fetch_cache_quota_mb == 0 {
		fetch_cache_quota_mb = 100
	}
	if session_days == 0 {
		session_days = 7
	}
	if !session_absolute_set {
		session_absolute_days = DefaultSessionAbsoluteDays
	}
	if max_attempts == 0 {
		max_attempts = 5
	}
	if lockout_minutes == 0 {
		lockout_minutes = 15
	}
	// Build the proxy URL from the configured port and external host (if set).
	// A loopback-bound proxy answers on localhost and nowhere else, so showing
	// the deployment's external hostname there would hand the operator a URL
	// that cannot work and read as the endpoint being broken.
	var proxy_url string
	if ollama_proxy_port > 0 {
		host := "localhost"
		if ollama_proxy_bind == "127.0.0.1" {
			external_url = ""
		}
		if external_url != "" {
			// Strip scheme and path, keep just the hostname.
			h := strings.TrimRight(external_url, "/")
			h = strings.TrimPrefix(h, "https://")
			h = strings.TrimPrefix(h, "http://")
			if slash := strings.Index(h, "/"); slash >= 0 {
				h = h[:slash]
			}
			if colon := strings.Index(h, ":"); colon >= 0 {
				h = h[:colon]
			}
			if h != "" {
				host = h
			}
		}
		proxy_url = fmt.Sprintf("http://%s:%d", host, ollama_proxy_port)
	}
	// Only expose proxy config when Ollama is the active provider.
	ollama_active := OllamaBackendFunc != nil
	if ollama_active {
		_, m, _ := OllamaBackendFunc()
		ollama_active = m != ""
	}
	ui_theme := AuthGetUITheme(a.db)
	if ui_theme == "" {
		ui_theme = "indigo"
	}
	w.Header().Set("Content-Type", "application/json")
	resp := map[string]interface{}{
		"allow_signup":          allow_signup,
		"session_days":          session_days,
		"session_absolute_days": session_absolute_days,
		"max_login_attempts":    max_attempts,
		"lockout_minutes":       lockout_minutes,
		"service_name":          service_name,
		"external_url":          external_url,
		"notify_from":           notify_from,
		"default_apps":          AuthGetDefaultApps(a.db),
		"ollama_proxy_enabled":  ollama_proxy_enabled,
		"ollama_proxy_port":     ollama_proxy_port,
		"ollama_proxy_bind":     ollama_proxy_bind,
		"ollama_proxy_url":      proxy_url,
		"ollama_active":         ollama_active,
		"fetch_cache_quota_mb":  fetch_cache_quota_mb,
		"channel_wake_rules":    AuthGetChannelWakeRules(a.db),
		"ui_theme":              ui_theme,
		"doc_brand":             AuthGetDocBrand(a.db),
		"site_name":             AuthGetSiteName(a.db),
		"timezone":              DeploymentTimezoneName(a.db),
	}
	// Tunables — effective values (stored override or spec default), generated
	// from the registry so a newly-registered knob surfaces here automatically.
	for _, s := range AllTunableSpecs() {
		if s.Kind == KindBool {
			resp[s.Key] = TunableEffectiveValue(s.Key) != 0 // toggle reads a bool
		} else {
			resp[s.Key] = TunableEffectiveValue(s.Key)
		}
	}
	json.NewEncoder(w).Encode(resp)
}

func (a *AdminApp) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AllowSignup         *bool     `json:"allow_signup,omitempty"`
		SessionDays         *int      `json:"session_days,omitempty"`
		SessionAbsoluteDays *int      `json:"session_absolute_days,omitempty"`
		MaxLoginAttempts    *int      `json:"max_login_attempts,omitempty"`
		LockoutMinutes      *int      `json:"lockout_minutes,omitempty"`
		ServiceName         *string   `json:"service_name,omitempty"`
		ExternalURL         *string   `json:"external_url,omitempty"`
		NotifyFrom          *string   `json:"notify_from,omitempty"`
		DefaultApps         *[]string `json:"default_apps,omitempty"`
		OllamaProxyEnabled  *bool     `json:"ollama_proxy_enabled,omitempty"`
		OllamaProxyPort     *int      `json:"ollama_proxy_port,omitempty"`
		OllamaProxyBind     *string   `json:"ollama_proxy_bind,omitempty"`
		FetchCacheQuotaMB   *int      `json:"fetch_cache_quota_mb,omitempty"`
		ChannelWakeRules    *string   `json:"channel_wake_rules,omitempty"`
		UITheme             *string   `json:"ui_theme,omitempty"`
		DocBrand            *string   `json:"doc_brand,omitempty"`
		SiteName            *string   `json:"site_name,omitempty"`
		Timezone            *string   `json:"timezone,omitempty"`
	}
	// Read the body once: the static settings decode into the typed struct
	// above, the tunables come off the same bytes as a generic map (validated
	// against the registry below).
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	current := AuthCurrentUser(r)
	if req.AllowSignup != nil {
		a.db.Set(WebTable, "allow_signup", *req.AllowSignup)
		Log("[admin] user %q set allow_signup=%v", current, *req.AllowSignup)
	}
	if req.SessionAbsoluteDays != nil && *req.SessionAbsoluteDays >= 0 && *req.SessionAbsoluteDays <= 3650 {
		// Written even when 0 — that is the "no ceiling" choice, and storing it
		// is what keeps it from reading as unset on the next load.
		a.db.Set(WebTable, "session_absolute_days", *req.SessionAbsoluteDays)
		Log("[admin] user %q set session_absolute_days=%d", current, *req.SessionAbsoluteDays)
	}
	if req.SessionDays != nil && *req.SessionDays >= 1 && *req.SessionDays <= 90 {
		a.db.Set(WebTable, "session_days", *req.SessionDays)
		Log("[admin] user %q set session_days=%d", current, *req.SessionDays)
	}
	if req.MaxLoginAttempts != nil && *req.MaxLoginAttempts >= 1 && *req.MaxLoginAttempts <= 100 {
		a.db.Set(WebTable, "max_login_attempts", *req.MaxLoginAttempts)
		Log("[admin] user %q set max_login_attempts=%d", current, *req.MaxLoginAttempts)
	}
	if req.LockoutMinutes != nil && *req.LockoutMinutes >= 1 && *req.LockoutMinutes <= 1440 {
		a.db.Set(WebTable, "lockout_minutes", *req.LockoutMinutes)
		Log("[admin] user %q set lockout_minutes=%d", current, *req.LockoutMinutes)
	}
	if req.ServiceName != nil {
		a.db.Set(WebTable, "service_name", *req.ServiceName)
		Log("[admin] user %q set service_name=%q", current, *req.ServiceName)
	}
	if req.ExternalURL != nil {
		a.db.Set(WebTable, "external_url", *req.ExternalURL)
		Log("[admin] user %q set external_url=%q", current, *req.ExternalURL)
	}
	if req.NotifyFrom != nil {
		a.db.Set(WebTable, "notify_from", *req.NotifyFrom)
		Log("[admin] user %q set notify_from=%q", current, *req.NotifyFrom)
	}
	if req.DefaultApps != nil {
		AuthSetDefaultApps(a.db, *req.DefaultApps)
		Log("[admin] user %q set default_apps=%v", current, *req.DefaultApps)
	}
	if req.OllamaProxyEnabled != nil {
		a.db.Set(WebTable, "ollama_proxy_enabled", *req.OllamaProxyEnabled)
		Log("[admin] user %q set ollama_proxy_enabled=%v", current, *req.OllamaProxyEnabled)
	}
	if req.OllamaProxyPort != nil && *req.OllamaProxyPort >= 0 && *req.OllamaProxyPort <= 65535 {
		a.db.Set(WebTable, "ollama_proxy_port", *req.OllamaProxyPort)
		Log("[admin] user %q set ollama_proxy_port=%d", current, *req.OllamaProxyPort)
	}
	if req.OllamaProxyBind != nil {
		bind := strings.TrimSpace(*req.OllamaProxyBind)
		// Only the two answers the form offers. A free-typed address that does
		// not parse would silently fail to bind at next start, and the operator
		// would find out from a missing endpoint rather than from this form.
		if bind != "127.0.0.1" && bind != "0.0.0.0" {
			http.Error(w, "ollama_proxy_bind must be 127.0.0.1 (this machine only) or 0.0.0.0 (all interfaces)", http.StatusBadRequest)
			return
		}
		a.db.Set(WebTable, "ollama_proxy_bind", bind)
		// Logged at Warn when it widens: this is the one setting on the page
		// that turns a local endpoint into a network-reachable one.
		if bind == "0.0.0.0" {
			Warn("[admin] user %q exposed the Ollama proxy on all interfaces — requests from off-box now require an API key", current)
		} else {
			Log("[admin] user %q set ollama_proxy_bind=%s", current, bind)
		}
	}
	if req.FetchCacheQuotaMB != nil && *req.FetchCacheQuotaMB >= 0 && *req.FetchCacheQuotaMB <= 10240 {
		a.db.Set(WebTable, "fetch_cache_quota_mb", *req.FetchCacheQuotaMB)
		Log("[admin] user %q set fetch_cache_quota_mb=%d", current, *req.FetchCacheQuotaMB)
	}
	if req.ChannelWakeRules != nil {
		AuthSetChannelWakeRules(a.db, strings.TrimSpace(*req.ChannelWakeRules))
		Log("[admin] user %q updated channel_wake_rules (%d chars)", current, len(strings.TrimSpace(*req.ChannelWakeRules)))
	}
	if req.UITheme != nil {
		// Validate against the theme registry (core/ui) so a typo can't blank
		// the whole UI — and so it stays correct as themes are added.
		if t := strings.TrimSpace(*req.UITheme); ui.IsValidTheme(t) {
			AuthSetUITheme(a.db, t)
			Log("[admin] user %q set ui_theme=%q", current, t)
		} else {
			Log("[admin] user %q tried to set unknown ui_theme=%q — ignored", current, t)
		}
	}
	if req.DocBrand != nil {
		AuthSetDocBrand(a.db, strings.TrimSpace(*req.DocBrand)) // also syncs PDFBranding live
		Log("[admin] user %q set doc_brand=%q", current, strings.TrimSpace(*req.DocBrand))
	}
	if req.SiteName != nil {
		AuthSetSiteName(a.db, strings.TrimSpace(*req.SiteName))
		Log("[admin] user %q set site_name=%q", current, strings.TrimSpace(*req.SiteName))
	}
	if req.Timezone != nil {
		// Validate against the zone resolver so a typo can't strand the
		// deployment in the wrong zone on next boot. Blank clears the override
		// (back to host zone). Stored as the canonical IANA name. Takes effect
		// on restart — reassigning time.Local live would race concurrent
		// formatting.
		if tz := strings.TrimSpace(*req.Timezone); tz == "" {
			a.db.Set(WebTable, TimezoneKey, "")
			Log("[admin] user %q cleared timezone (host zone; applies on restart)", current)
		} else if _, iana, err := ResolveZone(tz); err != nil {
			Log("[admin] user %q tried to set unknown timezone=%q — ignored", current, tz)
		} else {
			a.db.Set(WebTable, TimezoneKey, iana)
			Log("[admin] user %q set timezone=%q (applies on restart)", current, iana)
		}
	}
	// Tunables — validated against the registry, so adding a knob needs no
	// change here. A present numeric key within its spec's [Min, Max] is
	// stored as float64 (TuneInt casts); out-of-range or non-numeric is
	// silently ignored. Invalidate the cache once if anything changed.
	var generic map[string]any
	if json.Unmarshal(raw, &generic) == nil {
		tuned := false
		for _, s := range AllTunableSpecs() {
			v, ok := generic[s.Key]
			if !ok {
				continue
			}
			// Numbers decode as float64; a KindBool toggle POSTs true/false.
			var f float64
			switch val := v.(type) {
			case float64:
				f = val
			case bool:
				if s.Kind != KindBool {
					continue // a bool for a non-bool knob is malformed; ignore
				}
				if val {
					f = 1
				}
			default:
				continue
			}
			if f < s.Min || f > s.Max {
				continue
			}
			a.db.Set(WebTable, s.Key, f)
			Log("[admin] user %q set %s=%g", current, s.Key, f)
			tuned = true
		}
		if tuned {
			InvalidateTunables()
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "updated"})
}

// handleResetTunables clears every retrieval/limit tunable override so the
// getters fall back to their code defaults. Backs the "Revert to defaults"
// button on the admin Retrieval & limits panel.
func (a *AdminApp) handleResetTunables(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// Optional ?category=X scopes the revert to one section's knobs; absent,
	// every tunable resets. Either way the getters fall back to spec defaults.
	cat := r.URL.Query().Get("category")
	for _, s := range AllTunableSpecs() {
		if cat == "" || s.Category == cat {
			a.db.Unset(WebTable, s.Key)
		}
	}
	InvalidateTunables()
	Log("[admin] user %q reverted tunables to defaults (category=%q)", AuthCurrentUser(r), cat)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "reset"})
}

// handleListApps returns all registered web apps (excluding admin and
// any app that implements WebHidden() returning true) for the app
// assignment UI. Hidden apps are routing-only surfaces (e.g. the
// /agents/ umbrella that fans out to per-slug exposed agents) — they
// shouldn't appear as togglable items in the per-user / default-apps
// pickers.
func (a *AdminApp) handleListApps(w http.ResponseWriter, r *http.Request) {
	type appInfo struct {
		Path string `json:"path"`
		Name string `json:"name"`
	}
	type hidden interface{ WebHidden() bool }
	isHidden := func(wa WebApp) bool {
		if h, ok := wa.(hidden); ok && h.WebHidden() {
			return true
		}
		return false
	}
	var apps []appInfo
	for _, wa := range RegisteredWebApps() {
		if wa.WebPath() == "/admin" || isHidden(wa) {
			continue
		}
		apps = append(apps, appInfo{Path: wa.WebPath(), Name: wa.WebName()})
	}
	for _, ag := range RegisteredApps() {
		if wa, ok := ag.(WebApp); ok && wa.WebPath() != "/admin" && !isHidden(wa) {
			apps = append(apps, appInfo{Path: wa.WebPath(), Name: wa.WebName()})
		}
	}
	for _, ag := range RegisteredAgents() {
		if wa, ok := ag.(WebApp); ok && wa.WebPath() != "/admin" && !isHidden(wa) {
			apps = append(apps, appInfo{Path: wa.WebPath(), Name: wa.WebName()})
		}
	}
	// Dynamic grantable apps — surfaces (like orchestrate) that
	// produce one logical "app" per record (e.g. each exposed agent
	// at /agents/<slug>) implement GrantableAppListSource so we can
	// surface them in the user-apps picker. Without this, admins
	// can't grant per-agent access through the standard permission UI.
	for _, ag := range RegisteredApps() {
		if src, ok := ag.(GrantableAppListSource); ok {
			for _, ga := range src.ListGrantableApps() {
				apps = append(apps, appInfo{Path: ga.Path, Name: ga.Name})
			}
		}
	}
	// Deduplicate.
	seen := make(map[string]bool)
	var unique []appInfo
	for _, ap := range apps {
		if !seen[ap.Path] {
			seen[ap.Path] = true
			unique = append(unique, ap)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(unique)
}
