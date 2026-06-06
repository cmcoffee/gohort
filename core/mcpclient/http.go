package mcpclient

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
)

// Authorizer mutates an outgoing HTTP request to attach credentials
// (typically an Authorization header). It runs per request so tokens
// can be minted/refreshed at call time. nil means no auth.
type Authorizer func(req *http.Request) error

// HTTPTransport implements Transport over MCP "Streamable HTTP": each
// JSON-RPC message is a POST to a single endpoint, and the response is
// either an application/json body (one message) or a text/event-stream
// (SSE) carrying the message. The server may hand back an Mcp-Session-Id
// on initialize that we echo on every subsequent request.
type HTTPTransport struct {
	url    string
	client *http.Client
	auth   Authorizer

	mu        sync.Mutex
	sessionID string
}

// HTTPOptions configures a Streamable-HTTP transport.
type HTTPOptions struct {
	// Auth attaches credentials per request (bearer token, OAuth, …).
	Auth Authorizer
	// Client overrides the default *http.Client (e.g. for timeouts/proxy).
	Client *http.Client
}

// NewHTTPTransport builds a Streamable-HTTP transport pointed at url.
func NewHTTPTransport(url string, opts HTTPOptions) *HTTPTransport {
	c := opts.Client
	if c == nil {
		// No client-level timeout: per-call deadlines come from the
		// context passed to Call/Notify, which also covers SSE streams
		// that a fixed client timeout would cut mid-response.
		c = &http.Client{}
	}
	return &HTTPTransport{url: url, client: c, auth: opts.Auth}
}

// Call POSTs a request frame and returns the JSON-RPC response frame.
func (t *HTTPTransport) Call(ctx context.Context, frame []byte) ([]byte, error) {
	resp, ct, err := t.post(ctx, frame)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	t.captureSession(resp)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	if strings.Contains(ct, "text/event-stream") {
		return readSSEResponse(ctx, resp.Body)
	}
	// Default: a single JSON-RPC response object.
	return io.ReadAll(resp.Body)
}

// Notify POSTs a notification frame and discards the (usually empty,
// 202 Accepted) response.
func (t *HTTPTransport) Notify(ctx context.Context, frame []byte) error {
	resp, _, err := t.post(ctx, frame)
	if err != nil {
		return err
	}
	t.captureSession(resp)
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("http %d", resp.StatusCode)
	}
	return nil
}

// Close releases idle connections.
func (t *HTTPTransport) Close() error {
	t.client.CloseIdleConnections()
	return nil
}

// post builds and sends one POST, returning the response and its
// Content-Type. The caller closes the body.
func (t *HTTPTransport) post(ctx context.Context, frame []byte) (*http.Response, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.url, bytes.NewReader(frame))
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", protocolVersion)
	t.mu.Lock()
	sid := t.sessionID
	t.mu.Unlock()
	if sid != "" {
		req.Header.Set("Mcp-Session-Id", sid)
	}
	if t.auth != nil {
		if err := t.auth(req); err != nil {
			return nil, "", fmt.Errorf("authorize: %w", err)
		}
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return nil, "", err
	}
	return resp, resp.Header.Get("Content-Type"), nil
}

// captureSession records the Mcp-Session-Id the server assigns (on
// initialize) so subsequent requests carry it.
func (t *HTTPTransport) captureSession(resp *http.Response) {
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		t.mu.Lock()
		t.sessionID = sid
		t.mu.Unlock()
	}
}

// readSSEResponse scans an SSE stream and returns the data of the first
// event that is a JSON-RPC response (has an id and a result or error),
// skipping any server-initiated notifications/requests that precede it.
func readSSEResponse(ctx context.Context, body io.Reader) ([]byte, error) {
	sc := bufio.NewScanner(body)
	// Allow large event payloads (search results can be sizable).
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	var data strings.Builder
	flush := func() ([]byte, bool) {
		if data.Len() == 0 {
			return nil, false
		}
		payload := []byte(data.String())
		data.Reset()
		if isRPCResponse(payload) {
			return payload, true
		}
		return nil, false
	}
	for sc.Scan() {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		line := sc.Text()
		if line == "" { // event boundary
			if out, ok := flush(); ok {
				return out, nil
			}
			continue
		}
		if strings.HasPrefix(line, ":") { // comment/keep-alive
			continue
		}
		if v, ok := cutSSEField(line, "data:"); ok {
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(v)
		}
		// other fields (event:, id:, retry:) are not needed here
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if out, ok := flush(); ok { // stream ended without trailing blank line
		return out, nil
	}
	return nil, fmt.Errorf("sse stream ended without a JSON-RPC response")
}

// cutSSEField strips an SSE field prefix and the single optional leading
// space the spec allows after the colon.
func cutSSEField(line, prefix string) (string, bool) {
	if !strings.HasPrefix(line, prefix) {
		return "", false
	}
	v := strings.TrimPrefix(line, prefix)
	v = strings.TrimPrefix(v, " ")
	return v, true
}

// isRPCResponse reports whether payload is a JSON-RPC response object
// (carries an id and a result or error) rather than a server-initiated
// notification (no id) or request (has a method).
func isRPCResponse(payload []byte) bool {
	var probe struct {
		ID     json.RawMessage `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
		Method string          `json:"method"`
	}
	if json.Unmarshal(payload, &probe) != nil {
		return false
	}
	if probe.Method != "" { // server request/notification
		return false
	}
	return len(probe.ID) > 0 && (len(probe.Result) > 0 || len(probe.Error) > 0)
}

// compile-time guard
var _ Transport = (*HTTPTransport)(nil)
