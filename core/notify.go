package core

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"os"
	"strings"
)

// NotifyFromFunc returns a custom from address for notification emails.
// When set and non-empty, overrides the mail config's From address.
var NotifyFromFunc func() string

// SendNotification sends an email notification using the configured SMTP
// settings. Returns nil if mail is not configured (silent no-op). Background
// callers (a report, an alert) have no request to inherit; a handler that
// does should call MailConfig.SendNotification with its own context so a
// cancelled test does not leave an SMTP session running to its own timeout.
func SendNotification(to, subject, body string) error {
	return LoadMailConfig().SendNotification(context.Background(), to, subject, body)
}

// SendNotification sends one message with THIS config, under ctx. The
// context covers the whole SMTP conversation — dial, STARTTLS, auth, the
// data transfer — not just the dial: net/smtp's SendMail takes no context,
// so a test against a host that accepts and then says nothing could only be
// ended by the server's timeout. Returns nil when mail is not configured.
func (cfg MailConfig) SendNotification(ctx context.Context, to, subject, body string) error {
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

	host := server
	if idx := strings.Index(host, ":"); idx > 0 {
		host = host[:idx]
	}
	var auth smtp.Auth
	if cfg.Username != "" && cfg.Password != "" {
		auth = smtp.PlainAuth("", cfg.Username, cfg.Password, host)
	}

	if err := sendMailCtx(ctx, server, host, auth, from, to, []byte(msg)); err != nil {
		Log("[notify] failed to send to %s: %v", to, err)
		return err
	}
	Log("[notify] sent to %s: %s", to, subject)
	return nil
}

// sendMailCtx is net/smtp.SendMail with a context: the same conversation
// (EHLO, STARTTLS when the server offers it, AUTH when given, MAIL, RCPT,
// DATA, QUIT), dialled under ctx and with the connection closed the moment
// ctx ends, so a blocked read anywhere in the exchange returns instead of
// waiting on the peer. The context's error is reported in preference to the
// I/O error the close provokes, since it is the one that explains the exit.
func sendMailCtx(ctx context.Context, addr, host string, auth smtp.Auth, from, to string, msg []byte) error {
	if ctx == nil {
		ctx = context.Background()
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			conn.Close()
		case <-done:
		}
	}()
	withCtx := func(err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		return withCtx(err)
	}
	defer c.Close()
	if ok, _ := c.Extension("STARTTLS"); ok {
		if err := c.StartTLS(&tls.Config{ServerName: host}); err != nil {
			return withCtx(err)
		}
	}
	if auth != nil {
		if ok, _ := c.Extension("AUTH"); ok {
			if err := c.Auth(auth); err != nil {
				return withCtx(err)
			}
		}
	}
	if err := c.Mail(from); err != nil {
		return withCtx(err)
	}
	if err := c.Rcpt(to); err != nil {
		return withCtx(err)
	}
	w, err := c.Data()
	if err != nil {
		return withCtx(err)
	}
	if _, err := w.Write(msg); err != nil {
		return withCtx(err)
	}
	if err := w.Close(); err != nil {
		return withCtx(err)
	}
	return withCtx(c.Quit())
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
