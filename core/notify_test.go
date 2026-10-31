package core

import (
	"context"
	"strings"
	"testing"

	"github.com/cmcoffee/gohort/core/media"
)

// A connector that sends mail sends the notifications: from the address SMTP
// would use (the web override, then the mail settings' From), with no SMTP
// server needed, and a failure comes back to the caller.
func TestNotificationsGoThroughAMailConnector(t *testing.T) {
	prevSvc, prevFrom := media.MailService, NotifyFromFunc
	t.Cleanup(func() { media.MailService, NotifyFromFunc = prevSvc, prevFrom })
	var sent []string
	media.MailService = func(_ context.Context, connector, from, to, subject, body string) error {
		sent = append(sent, strings.Join([]string{connector, from, to, subject, body}, "|"))
		return nil
	}
	NotifyFromFunc = nil
	cfg := MailConfig{Provider: "service:postbox", From: "alerts@mail.example"}
	if err := cfg.SendNotification(context.Background(), "pat@mail.example", "Rates", "Rates rose."); err != nil || len(sent) != 1 || sent[0] != "postbox|alerts@mail.example|pat@mail.example|Rates|Rates rose." {
		t.Errorf("sent %v %v", sent, err)
	}
	NotifyFromFunc = func() string { return "web@mail.example" }
	cfg.SendNotification(context.Background(), "pat@mail.example", "Rates", "x")
	if !strings.HasPrefix(sent[1], "postbox|web@mail.example|") {
		t.Errorf("the web From override: %v", sent)
	}
	media.MailService = nil
	if err := cfg.SendNotification(context.Background(), "pat@mail.example", "Rates", "x"); err == nil || !strings.Contains(err.Error(), "no connectors linked") {
		t.Errorf("with no connectors linked: %v", err)
	}
}
