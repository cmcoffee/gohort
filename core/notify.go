package core

import (
	"fmt"
	"net/smtp"
	"os"
	"strings"
)

// NotifyFromFunc returns a custom from address for notification emails.
// When set and non-empty, overrides the mail config's From address.
var NotifyFromFunc func() string

// SendNotification sends an email notification using the configured SMTP
// settings. Returns nil if mail is not configured (silent no-op).
func SendNotification(to, subject, body string) error {
	cfg := LoadMailConfig()
	if cfg.Server == "" && cfg.From == "" {
		// Mail not configured, skip silently.
		return nil
	}

	server := cfg.Server
	if server == "" {
		server = "localhost:25"
	}

	// Notification from address: web config override > mail config > default.
	from := ""
	if NotifyFromFunc != nil {
		from = NotifyFromFunc()
	}
	if from == "" {
		from = cfg.From
	}
	if from == "" {
		hostname, _ := os.Hostname()
		from = fmt.Sprintf("gohort@%s", hostname)
	}

	// Format the From header with a display name if available.
	from_header := from
	if name := ServiceName(); name != "" && !strings.Contains(from, "<") {
		from_header = fmt.Sprintf("%s <%s>", name, from)
	}

	msg := strings.Join([]string{
		fmt.Sprintf("From: %s", from_header),
		fmt.Sprintf("To: %s", to),
		fmt.Sprintf("Subject: %s", subject),
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=\"utf-8\"",
		"",
		body,
	}, "\r\n")

	var auth smtp.Auth
	if cfg.Username != "" && cfg.Password != "" {
		host := server
		if idx := strings.Index(host, ":"); idx > 0 {
			host = host[:idx]
		}
		auth = smtp.PlainAuth("", cfg.Username, cfg.Password, host)
	}

	if err := smtp.SendMail(server, auth, from, []string{to}, []byte(msg)); err != nil {
		Log("[notify] failed to send to %s: %v", to, err)
		return err
	}
	Log("[notify] sent to %s: %s", to, subject)
	return nil
}

// NotifyAdmin sends a notification to all admin users who have email
// addresses configured as their username. Optional exclude usernames
// are skipped to avoid duplicate notifications.
func NotifyAdmin(subject, body string, exclude ...string) {
	if AuthDB == nil {
		return
	}
	skip := make(map[string]bool)
	for _, e := range exclude {
		skip[e] = true
	}
	db := AuthDB()
	for _, u := range AuthListUsers(db) {
		if u.Admin && isValidEmail(u.Username) && !skip[u.Username] {
			go SendNotification(u.Username, subject, body)
		}
	}
}

// NotifyUser sends a notification to a specific user if their username
// is a valid email address.
func NotifyUser(username, subject, body string) {
	if isValidEmail(username) {
		go SendNotification(username, subject, body)
	}
}

// WebBaseURL returns the external-facing base URL for the dashboard.
// Falls back to constructing one from WebListenAddr and TLS state.
var WebBaseURL func() string

// ServiceNameFunc returns the configured service name for notifications.
// Defaults to "Gohort".
var ServiceNameFunc func() string

// ServiceName returns the configured service name for email subjects and bodies.
func ServiceName() string {
	if ServiceNameFunc != nil {
		if name := ServiceNameFunc(); name != "" {
			return name
		}
	}
	return "Gohort"
}

// DashboardURL returns the base URL for constructing links in notifications.
func DashboardURL() string {
	if WebBaseURL != nil {
		if u := WebBaseURL(); u != "" {
			return strings.TrimSuffix(u, "/")
		}
	}
	scheme := "http"
	if TLSEnabled() {
		scheme = "https"
	}
	return scheme + "://" + WebListenAddr
}
