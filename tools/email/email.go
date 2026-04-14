package email

import (
	"fmt"
	"net/smtp"
	"os"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

func init() { RegisterChatTool(new(EmailTool)) }

// EmailTool sends an email via SMTP using stored mail configuration.
type EmailTool struct{}

func (t *EmailTool) Name() string { return "send_email" }
func (t *EmailTool) Desc() string {
	return "Send an email to a recipient with a subject and body. The recipient email address must be known or explicitly provided by the user — never guess or fabricate an email address."
}

func (t *EmailTool) Params() map[string]ToolParam {
	return map[string]ToolParam{
		"to":      {Type: "string", Description: "Recipient email address."},
		"subject": {Type: "string", Description: "Email subject line."},
		"body":    {Type: "string", Description: "Email body text."},
	}
}

// NeedsConfirm implements ConfirmableTool — sending email always requires user approval.
func (t *EmailTool) NeedsConfirm() bool { return true }

func (t *EmailTool) Run(args map[string]any) (string, error) {
	to := StringArg(args, "to")
	subject := StringArg(args, "subject")
	body := StringArg(args, "body")

	if to == "" {
		return "", fmt.Errorf("'to' is required")
	}
	if subject == "" {
		return "", fmt.Errorf("'subject' is required")
	}
	if body == "" {
		return "", fmt.Errorf("'body' is required")
	}

	// Load mail config from database.
	cfg := LoadMailConfig()

	server := cfg.Server
	if server == "" {
		server = "localhost:25"
	}

	from := cfg.From
	if from == "" {
		hostname, _ := os.Hostname()
		from = fmt.Sprintf("fuzz@%s", hostname)
	}

	msg := strings.Join([]string{
		fmt.Sprintf("From: %s", from),
		fmt.Sprintf("To: %s", to),
		fmt.Sprintf("Subject: %s", subject),
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=\"utf-8\"",
		"",
		body,
	}, "\r\n")

	// Set up auth if credentials are configured.
	var auth smtp.Auth
	if cfg.Username != "" && cfg.Password != "" {
		host := server
		if idx := strings.Index(host, ":"); idx > 0 {
			host = host[:idx]
		}
		auth = smtp.PlainAuth("", cfg.Username, cfg.Password, host)
	}

	err := smtp.SendMail(server, auth, from, []string{to}, []byte(msg))
	if err != nil {
		return "", fmt.Errorf("failed to send email: %w", err)
	}

	return fmt.Sprintf("Email sent to %s.", to), nil
}
