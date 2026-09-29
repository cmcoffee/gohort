package bridges

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func genericSpec(wh RestMessagingWebhook) RestMessagingSpec {
	return RestMessagingSpec{Service: "chatx", WebhookProvider: "generic", Webhook: &wh,
		Map: RestMessagingFieldMap{ChatID: "room.id", Sender: "from", Text: "text", MsgID: "id"}}
}

// A signed push passes only with the right signature, in the encoding and
// prefix the spec names.
func TestGenericWebhookChecksASignature(t *testing.T) {
	body := []byte(`{"id":"1","room":{"id":"r"},"from":"ann","text":"hi"}`)
	mac := hmac.New(sha256.New, []byte("s3cret"))
	mac.Write(body)
	sum := mac.Sum(nil)
	for name, tc := range map[string]struct {
		wh  RestMessagingWebhook
		sig string
	}{
		"hex with a prefix": {RestMessagingWebhook{Verify: "hmac_sha256", Header: "X-Sig", Prefix: "sha256="}, "sha256=" + hex.EncodeToString(sum)},
		"base64":            {RestMessagingWebhook{Verify: "hmac_sha256", Header: "X-Sig", Encoding: "base64"}, base64.StdEncoding.EncodeToString(sum)},
	} {
		r := httptest.NewRequest(http.MethodPost, "/", nil)
		r.Header.Set("X-Sig", tc.sig)
		if err := (genericProvider{}).verify(r, body, "s3cret", genericSpec(tc.wh)); err != nil {
			t.Errorf("%s: the right signature passes: %v", name, err)
		}
		if err := (genericProvider{}).verify(r, []byte(`{"text":"tampered"}`), "s3cret", genericSpec(tc.wh)); err == nil {
			t.Errorf("%s: a changed body fails", name)
		}
		if err := (genericProvider{}).verify(r, body, "other", genericSpec(tc.wh)); err == nil {
			t.Errorf("%s: another secret fails", name)
		}
	}
	if err := (genericProvider{}).verify(httptest.NewRequest(http.MethodPost, "/", nil), body, "s3cret",
		genericSpec(RestMessagingWebhook{Verify: "hmac_sha256", Header: "X-Sig"})); err == nil {
		t.Error("no signature, no entry")
	}
}

// A token push passes with the token in the header or the body the spec names.
func TestGenericWebhookChecksAToken(t *testing.T) {
	body := []byte(`{"token":"tk","room":{"id":"r"},"text":"hi"}`)
	inBody := genericSpec(RestMessagingWebhook{Verify: "token", TokenPath: "token"})
	if err := (genericProvider{}).verify(httptest.NewRequest(http.MethodPost, "/", nil), body, "tk", inBody); err != nil {
		t.Errorf("the token in the body passes: %v", err)
	}
	if err := (genericProvider{}).verify(httptest.NewRequest(http.MethodPost, "/", nil), body, "other", inBody); err == nil {
		t.Error("a different token fails")
	}
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.Header.Set("X-Token", "Bearer tk")
	if err := (genericProvider{}).verify(r, body, "tk", genericSpec(RestMessagingWebhook{Verify: "token", Header: "X-Token", Prefix: "Bearer "})); err != nil {
		t.Errorf("the token in a header, prefix stripped, passes: %v", err)
	}
}

// A push is read by the spec's paths, one message or a list; skip rules drop
// the bridge's own posts; a handshake is answered only through checkedChallenge.
func TestGenericWebhookReadsAndSkips(t *testing.T) {
	spec := genericSpec(RestMessagingWebhook{Verify: "token", TokenPath: "token", ChallengePath: "challenge"})
	spec.Skip = []RestMessagingSkip{{Path: "from", Values: []string{"GOHORT"}}, {Path: "system"}}
	msgs, err := (genericProvider{}).extract([]byte(`{"id":"1","room":{"id":"r"},"from":"ann","text":"hi"}`), spec)
	if err != nil || len(msgs) != 1 || msgs[0].ChatID != "r" || msgs[0].Handle != "ann" || msgs[0].Text != "hi" {
		t.Fatalf("one message read by its paths: %+v %v", msgs, err)
	}
	for _, body := range []string{
		`{"room":{"id":"r"},"from":"gohort","text":"my own reply"}`,
		`{"room":{"id":"r"},"from":"ann","text":"joined","system":true}`,
	} {
		if msgs, _ := (genericProvider{}).extract([]byte(body), spec); len(msgs) != 0 {
			t.Errorf("skipped: %s", body)
		}
	}
	if msgs, _ := (genericProvider{}).extract([]byte(`{"room":{"id":"r"},"from":"ann","text":"x","system":false}`), spec); len(msgs) != 1 {
		t.Error("a false flag is not a match")
	}
	spec.ListPath = "events"
	if msgs, _ := (genericProvider{}).extract([]byte(`{"events":[{"room":{"id":"a"},"text":"1"},{"room":{"id":"b"},"text":"2"}]}`), spec); len(msgs) != 2 {
		t.Errorf("a list of messages: %+v", msgs)
	}

	if (genericProvider{}).challenge(httptest.NewRecorder(), nil, []byte(`{"challenge":"abc"}`)) {
		t.Error("no handshake is answered before the check")
	}
	w := httptest.NewRecorder()
	if !(genericProvider{}).checkedChallenge(w, []byte(`{"challenge":"abc"}`), spec) || w.Body.String() != "abc" {
		t.Errorf("the handshake value is echoed after the check: %q", w.Body.String())
	}
}

// Through the public route: a push with the wrong token is turned away, a
// handshake is answered once the token checks out, and a draft connector's
// pushes are acknowledged but go nowhere.
func TestGenericWebhookRoute(t *testing.T) {
	saveRoot := RootDB
	RootDB = OpenCache()
	t.Cleanup(func() { RootDB = saveRoot })
	T := &Bridges{AppCore{DB: OpenCache()}}
	spec := genericSpec(RestMessagingWebhook{Verify: "token", TokenPath: "token", ChallengePath: "challenge"})
	raw, _ := json.Marshal(spec)
	RootDB.Set("connectors", "hookx", Connector{Name: "hookx", Kind: RestMessagingConnectorKind, Owner: "ann", Approved: true, Spec: raw})
	T.setWebhookSecret("hookx", "tk")

	post := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		T.handleWebhook(w, httptest.NewRequest(http.MethodPost, "/api/webhook/hookx", strings.NewReader(body)))
		return w
	}
	if w := post(`{"token":"nope","challenge":"abc"}`); w.Code != http.StatusUnauthorized {
		t.Errorf("a wrong token is refused before anything else: %d %q", w.Code, w.Body.String())
	}
	if w := post(`{"token":"tk","challenge":"abc"}`); w.Code != http.StatusOK || w.Body.String() != "abc" {
		t.Errorf("the handshake is answered after the check: %d %q", w.Code, w.Body.String())
	}
	RootDB.Set("connectors", "hookx", Connector{Name: "hookx", Kind: RestMessagingConnectorKind, Owner: "ann", Spec: raw})
	if w := post(`{"token":"tk","room":{"id":"r"},"text":"hi"}`); w.Code != http.StatusAccepted {
		t.Errorf("a draft acknowledges and does nothing: %d", w.Code)
	}
}
