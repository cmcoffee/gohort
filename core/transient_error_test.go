package core

import (
	"errors"
	"net"
	"net/url"
	"syscall"
	"testing"
)

// A request that wrote into a connection the server had just closed comes
// back as a *url.Error around a *net.OpError. It is a net.Error whose Timeout
// and Temporary are false, and that answer was returned before the OpError
// check that exists to retry it.
func TestAStaleConnectionIsRetried(t *testing.T) {
	wrap := func(op string, errno error) error {
		return &url.Error{Op: "Post", URL: "http://llm.test/v1/chat/completions",
			Err: &net.OpError{Op: op, Net: "tcp", Err: errno}}
	}
	for name, err := range map[string]error{
		"broken pipe":      wrap("write", syscall.EPIPE),
		"connection reset": wrap("read", syscall.ECONNRESET),
	} {
		if !isTransientError(err) {
			t.Errorf("%s is not retried: %v", name, err)
		}
	}
	if isTransientError(errors.New("model not found")) {
		t.Error("a plain error is retried")
	}
}
