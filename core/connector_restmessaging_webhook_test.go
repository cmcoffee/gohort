package core

import "testing"

func TestGenericWebhookSpecIsValidated(t *testing.T) {
	spec := func(wh *RestMessagingWebhook) RestMessagingSpec {
		return RestMessagingSpec{Service: "chatx", WebhookProvider: "generic", Webhook: wh,
			Map: RestMessagingFieldMap{ChatID: "room.id", Text: "text"}}
	}
	if err := spec(&RestMessagingWebhook{Verify: "token", Header: "X-Token"}).validateGenericWebhook(); err != nil {
		t.Fatalf("a whole spec passes: %v", err)
	}
	noText := spec(&RestMessagingWebhook{Verify: "token", TokenPath: "token"})
	noText.Map.Text = ""
	for name, s := range map[string]RestMessagingSpec{
		"no check":       spec(&RestMessagingWebhook{Verify: "none", Header: "X"}),
		"hmac no header": spec(&RestMessagingWebhook{Verify: "hmac_sha256"}),
		"token nowhere":  spec(&RestMessagingWebhook{Verify: "token"}),
		"bad encoding":   spec(&RestMessagingWebhook{Verify: "hmac_sha256", Header: "X", Encoding: "b32"}),
		"no block":       spec(nil),
		"no text path":   noText,
	} {
		if s.validateGenericWebhook() == nil {
			t.Errorf("%s should be refused", name)
		}
	}
}
