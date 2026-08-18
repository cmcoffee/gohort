package core

import (
	"errors"
	"fmt"
	"testing"

	"github.com/cmcoffee/gohort/core/internal/mcpclient"
)

// The distinction the whole recovery turns on: a dead refresh token needs a
// human, a failed attempt needs another attempt. Getting this backwards either
// nags the user for nothing or wedges the credential silently.
func TestOAuthGrantRejected(t *testing.T) {
	terminal := []error{
		errors.New(`token endpoint 400: {"error":"invalid_grant"}`),
		errors.New("INVALID_GRANT: refresh token expired"),
		fmt.Errorf("wrapped: %w", errors.New(`{"error":"invalid_token"}`)),
		errors.New(`{"error":"unauthorized_client"}`),
	}
	for _, err := range terminal {
		if !oauthGrantRejected(err) {
			t.Errorf("should be terminal: %v", err)
		}
	}
	transient := []error{
		nil,
		errors.New("dial tcp: connection refused"),
		errors.New("token endpoint 500: internal server error"),
		errors.New("context deadline exceeded"),
		errors.New(`{"error":"temporarily_unavailable"}`),
	}
	for _, err := range transient {
		if oauthGrantRejected(err) {
			t.Errorf("should be retryable: %v", err)
		}
	}
}

// A 401 must arrive at the manager as a TYPED error. It used to be "http 401"
// in a string, and keying a recovery off error text is how a message reword
// silently turns the recovery off.
func TestUnauthorizedIsTyped(t *testing.T) {
	err := fmt.Errorf("%w (http %d: %s)", mcpclient.ErrUnauthorized, 401, "bad token")
	if !errors.Is(err, mcpclient.ErrUnauthorized) {
		t.Error("a wrapped 401 must still match ErrUnauthorized")
	}
	if errors.Is(err, mcpclient.ErrSessionExpired) {
		t.Error("a rejected CREDENTIAL is not an expired SESSION — they recover differently")
	}
}
