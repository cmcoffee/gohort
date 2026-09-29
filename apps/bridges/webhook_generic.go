package bridges

// The generic webhook provider: a push bridge described as data. The
// connector's spec says how a push is checked (an HMAC-SHA256 signature of the
// body, or a shared token in a header or the body), where a URL-verification
// handshake's value is, and where each field of a message is, so a service
// that pushes JSON needs no provider of its own. The secret it checks against
// is the connector's webhook secret, kept here encrypted, never in the spec.

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

type genericProvider struct{}

// challenge does nothing before the check: see checkedChallenge.
func (genericProvider) challenge(http.ResponseWriter, *http.Request, []byte) bool { return false }

func (genericProvider) verify(r *http.Request, body []byte, secret string, spec RestMessagingSpec) error {
	wh := spec.Webhook
	if wh == nil {
		return fmt.Errorf("the connector has no webhook block")
	}
	switch wh.Verify {
	case "hmac_sha256":
		got := strings.TrimSpace(r.Header.Get(wh.Header))
		if got == "" {
			return fmt.Errorf("no %s header", wh.Header)
		}
		got = strings.TrimPrefix(got, wh.Prefix)
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(body)
		sum := mac.Sum(nil)
		var want string
		if wh.Encoding == "base64" {
			want = base64.StdEncoding.EncodeToString(sum)
		} else {
			want = hex.EncodeToString(sum)
			got = strings.ToLower(got)
		}
		if !hmac.Equal([]byte(want), []byte(got)) {
			return fmt.Errorf("signature mismatch")
		}
		return nil
	case "token":
		var got string
		if wh.Header != "" {
			got = r.Header.Get(wh.Header)
		} else {
			var root any
			if json.Unmarshal(body, &root) == nil {
				got = jsonPathString(root, wh.TokenPath)
			}
		}
		got = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(got), wh.Prefix))
		if got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(secret)) != 1 {
			return fmt.Errorf("token mismatch")
		}
		return nil
	}
	return fmt.Errorf("unknown verify %q", wh.Verify)
}

// checkedChallenge answers a handshake the spec describes, once the push has
// passed its check: the value at challenge_path, as plain text.
func (genericProvider) checkedChallenge(w http.ResponseWriter, body []byte, spec RestMessagingSpec) bool {
	if spec.Webhook == nil || spec.Webhook.ChallengePath == "" {
		return false
	}
	var root any
	if json.Unmarshal(body, &root) != nil {
		return false
	}
	v := jsonPathString(root, spec.Webhook.ChallengePath)
	if v == "" || len(v) > 1024 {
		return false
	}
	w.Header().Set("Content-Type", "text/plain")
	_, _ = w.Write([]byte(v))
	return true
}

// extract reads the push's messages with the spec's list_path and map; a push
// that is one message (no list_path, an object) is read as a list of one.
func (genericProvider) extract(body []byte, spec RestMessagingSpec) ([]hookRequest, error) {
	var root any
	if err := json.Unmarshal(body, &root); err != nil {
		return nil, fmt.Errorf("the push is not JSON: %w", err)
	}
	if _, isObj := root.(map[string]any); isObj && spec.ListPath == "" {
		root = []any{root}
	}
	return messagesFromResponse(root, spec), nil
}

func (genericProvider) autoSecret() (string, bool) { return "", false } // the service gives it, or the admin chooses it
