package email

import (
	"context"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/media"
)

// send_email goes through a connector that sends mail when the mail settings
// name one, from the settings' From address, with no SMTP server involved.
func TestSendEmailGoesThroughAMailConnector(t *testing.T) {
	prevCfg, prevSvc := LoadMailConfigFunc, media.MailService
	t.Cleanup(func() { LoadMailConfigFunc, media.MailService = prevCfg, prevSvc })
	LoadMailConfigFunc = func() MailConfig { return MailConfig{Provider: "service:postbox", From: "alerts@mail.example"} }
	var sent string
	media.MailService = func(_ context.Context, connector, from, to, subject, body string) error {
		sent = strings.Join([]string{connector, from, to, subject, body}, "|")
		return nil
	}
	out, err := new(EmailTool).Run(map[string]any{"to": "pat@mail.example", "subject": "Rates", "body": "Rates rose."})
	if err != nil || out != "Email sent to pat@mail.example." || sent != "postbox|alerts@mail.example|pat@mail.example|Rates|Rates rose." {
		t.Errorf("%q %v, sent %q", out, err, sent)
	}
}
