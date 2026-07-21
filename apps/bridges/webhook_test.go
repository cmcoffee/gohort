package bridges

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

func slackSig(secret, ts string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("v0:" + ts + ":"))
	mac.Write(body)
	return "v0=" + hex.EncodeToString(mac.Sum(nil))
}

func slackReq(secret, ts string, body []byte) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/webhook/x", nil)
	r.Header.Set("X-Slack-Request-Timestamp", ts)
	r.Header.Set("X-Slack-Signature", slackSig(secret, ts, body))
	return r
}

func TestSlackVerify(t *testing.T) {
	const secret = "shhh-signing-secret"
	body := []byte(`{"type":"event_callback"}`)
	now := strconv.FormatInt(time.Now().Unix(), 10)

	if err := (slackProvider{}).verify(slackReq(secret, now, body), body, secret); err != nil {
		t.Errorf("valid signature rejected: %v", err)
	}
	// Stale timestamp → replay guard trips.
	old := strconv.FormatInt(time.Now().Unix()-600, 10)
	if err := (slackProvider{}).verify(slackReq(secret, old, body), body, secret); err == nil {
		t.Error("stale timestamp accepted")
	}
	// Tampered body → signature no longer matches.
	r := slackReq(secret, now, body)
	if err := (slackProvider{}).verify(r, []byte(`{"type":"tampered"}`), secret); err == nil {
		t.Error("tampered body accepted")
	}
	// Wrong secret → mismatch.
	if err := (slackProvider{}).verify(slackReq("other", now, body), body, secret); err == nil {
		t.Error("wrong secret accepted")
	}
}

func TestGraphChannelResource(t *testing.T) {
	// Derived from the teams preset's send_url.
	got, err := graphChannelResource(RestMessagingSpec{SendURL: "https://graph.microsoft.com/v1.0/teams/T/channels/C/messages"})
	if err != nil || got != "teams/T/channels/C/messages" {
		t.Errorf("resource = %q, err = %v", got, err)
	}
	if _, err := graphChannelResource(RestMessagingSpec{SendURL: "https://example.com/x"}); err == nil {
		t.Error("non-graph send_url should error")
	}
	if _, err := graphChannelResource(RestMessagingSpec{}); err == nil {
		t.Error("empty send_url should error")
	}
}

func TestSlackChallenge(t *testing.T) {
	w := httptest.NewRecorder()
	body := []byte(`{"type":"url_verification","challenge":"c-42"}`)
	if !(slackProvider{}).challenge(w, httptest.NewRequest(http.MethodPost, "/", nil), body) {
		t.Fatal("url_verification not recognized")
	}
	if w.Body.String() != "c-42" {
		t.Errorf("challenge echo = %q, want c-42", w.Body.String())
	}
	// A normal event is not a challenge.
	if (slackProvider{}).challenge(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", nil), []byte(`{"type":"event_callback"}`)) {
		t.Error("event_callback treated as challenge")
	}
}

func TestSlackExtractFiltering(t *testing.T) {
	cases := []struct {
		name string
		body string
		want int
	}{
		{"user message", `{"type":"event_callback","event":{"type":"message","channel":"C1","user":"U1","text":"hi","ts":"1.1"}}`, 1},
		{"own bot reply", `{"type":"event_callback","event":{"type":"message","channel":"C1","bot_id":"B1","text":"echo","ts":"1.2"}}`, 0},
		{"edited message", `{"type":"event_callback","event":{"type":"message","subtype":"message_changed","channel":"C1","text":"x","ts":"1.3"}}`, 0},
		{"non-message", `{"type":"event_callback","event":{"type":"reaction_added","channel":"C1"}}`, 0},
		{"empty text", `{"type":"event_callback","event":{"type":"message","channel":"C1","user":"U1","text":"  ","ts":"1.4"}}`, 0},
	}
	for _, tc := range cases {
		got, err := (slackProvider{}).extract([]byte(tc.body), RestMessagingSpec{})
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if len(got) != tc.want {
			t.Errorf("%s: extracted %d, want %d", tc.name, len(got), tc.want)
		}
	}
	// The mapped user message carries the right fields.
	msgs, _ := (slackProvider{}).extract([]byte(cases[0].body), RestMessagingSpec{})
	if len(msgs) == 1 && (msgs[0].ChatID != "C1" || msgs[0].Handle != "U1" || msgs[0].Text != "hi" || msgs[0].MsgID != "1.1") {
		t.Errorf("mismapped: %+v", msgs[0])
	}
}
