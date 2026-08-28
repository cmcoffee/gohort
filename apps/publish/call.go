// The one outbound HTTP path the destinations share.
//
// Every call goes through SecureAPI dispatch rather than a raw http.Client, and
// that is deliberate: dispatch is where the credential's Base URL + allowed
// endpoints are enforced, where the secret is attached (api key, basic, oauth2,
// or a per-user 3LO token resolved from sess.Username), where the call is
// audited, and where an admin disabling the credential actually stops the
// traffic. A destination that opened its own socket would quietly bypass all of
// it.
package publish

import (
	"fmt"
	"strconv"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// truncationMarker is what dispatch appends when a response exceeded its read
// cap. It would make a JSON body unparseable, so it's detected and reported as
// itself rather than surfacing as a confusing syntax error.
const truncationMarker = "... [TRUNCATED"

// callAPI makes an authorized request through the named credential and returns
// the response body. A non-2xx status becomes an error carrying what the server
// said, since that text is the only thing that tells an admin whether the
// problem is the path, the scope, or the credential.
//
// user is threaded through so a per-user credential resolves to THIS person's
// connected account: publishing happens as whoever clicked Publish.
func callAPI(user, credential, method, url, body string) ([]byte, error) {
	s := Secure()
	if s == nil {
		return nil, fmt.Errorf("the credential store is not initialized")
	}
	credential = strings.TrimSpace(credential)
	if credential == "" {
		return nil, fmt.Errorf("no credential is configured for this destination")
	}
	out, err := s.DispatchToolCallCT(&ToolSession{Username: user}, credential, url, method, body, "application/json")
	if err != nil {
		return nil, err
	}
	status, payload := splitHTTPEnvelope(out)
	if status == 0 {
		return nil, fmt.Errorf("unexpected response from %s: %s", url, firstLine(out))
	}
	if status >= 400 {
		return nil, fmt.Errorf("%s returned HTTP %d: %s", url, status, trimForError(payload))
	}
	if strings.Contains(payload, truncationMarker) {
		return nil, fmt.Errorf("the response from %s was too large to read in full", url)
	}
	return []byte(payload), nil
}

// splitHTTPEnvelope pulls the status code and body out of dispatch's
// "HTTP <code> <text>\n<body>" reply. A reply that doesn't start that way
// returns status 0, which the caller reports rather than guessing at.
func splitHTTPEnvelope(out string) (int, string) {
	head, rest, _ := strings.Cut(out, "\n")
	fields := strings.Fields(head)
	if len(fields) < 2 || fields[0] != "HTTP" {
		return 0, out
	}
	code, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0, out
	}
	return code, rest
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}

// trimForError keeps an error message readable — enough of the server's reply
// to diagnose it, not a whole page of JSON.
func trimForError(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 600 {
		return s[:600] + "…"
	}
	if s == "" {
		return "(no response body)"
	}
	return s
}
