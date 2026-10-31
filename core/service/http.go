package service

// The http transport: every request a service makes goes through the governed
// dispatch a credential's call takes (the allowlist, Private mode, the audit,
// the response limits), or, for a service that takes no key, the same path
// scoped to the service's own host. A result URL on another host (a CDN, a
// signed link) is fetched with no credential, through the client that refuses
// a non-public address after DNS resolution, so a reply cannot steer gohort
// into its own network or carry the key to another host.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/flows"
	"github.com/cmcoffee/gohort/core/sourcehooks"
)

type transport struct {
	p    Provider
	sess *ToolSession
}

// reply is one operation's answer: its status and, by what the operation
// reads, the parsed JSON, the text, or the file.
type reply struct {
	status int
	node   any
	text   string
	file   *File
}

func (t transport) credential() string { return strings.TrimSpace(t.p.Auth.Credential) }

func (t transport) timeout(op Operation) int {
	limit := 0
	if t.p.Limits != nil {
		limit = t.p.Limits.TimeoutSecs
	}
	for _, n := range []int{op.TimeoutSecs, limit} {
		if n > 0 {
			return n
		}
	}
	return defaultTimeout
}

func (t transport) maxBytes() int64 {
	if t.p.Limits != nil && t.p.Limits.MaxBytes > 0 {
		return t.p.Limits.MaxBytes
	}
	return defaultMaxBytes
}

// do makes the named operation with vars filling its tokens.
func (t transport) do(ctx context.Context, name string, vars map[string]any) (reply, error) {
	op := t.p.Operations[name]
	if t.p.Transport == "command" {
		return t.command(ctx, name, op, vars)
	}
	addr, err := t.address(op, vars)
	if err != nil {
		return reply{}, err
	}
	method := strings.ToUpper(strings.TrimSpace(op.Method))
	if method == "" {
		method = "GET"
		if op.Body != nil {
			method = "POST"
		}
	}
	// A file going up as the whole body, or a reply that is a file, takes
	// the raw path: the governed stream for a credential, the service's own
	// host for a keyless one.
	if op.Send == "file" || op.Read == "file" {
		var body io.Reader
		ctype := ""
		if op.Send == "file" {
			f, err := fileVar(vars, tokenName(op.Body.(string)))
			if err != nil {
				return reply{}, fmt.Errorf("%s: %w", name, err)
			}
			fh, err := os.Open(f.Path)
			if err != nil {
				return reply{}, fmt.Errorf("%s: %w", name, err)
			}
			defer fh.Close()
			body, ctype = fh, f.Type
		} else {
			// A JSON or form body, with a reply that is a file (a voice, a
			// song): the body goes on the raw path too.
			text, ct, err := renderRequest(op, vars)
			if err != nil {
				return reply{}, err
			}
			if text != "" {
				body, ctype = strings.NewReader(text), ct
			}
		}
		resp, done, err := t.raw(ctx, addr, method, body, ctype)
		if err != nil {
			return reply{}, fmt.Errorf("%s: %w", name, err)
		}
		return t.readResponse(name, op, resp, done)
	}
	req := ToolCallRequest{Credential: t.credential(), URL: addr, Method: method,
		Headers: t.p.Headers, PipeFollowing: true, TimeoutSecs: t.timeout(op)}
	if op.Send == "multipart" {
		up, closer, err := multipartUpload(op, vars, t.maxBytes())
		if err != nil {
			return reply{}, fmt.Errorf("%s: %w", name, err)
		}
		if closer != nil {
			defer closer.Close()
		}
		req.Upload = up
	} else if req.Body, req.ContentType, err = renderRequest(op, vars); err != nil {
		return reply{}, err
	}
	var raw string
	if req.Credential != "" {
		raw, err = Secure().DispatchToolCallRequest(t.sess, req)
	} else {
		raw, err = Secure().DispatchHostScoped(t.sess, t.p.BaseURL, req)
	}
	if err != nil {
		return reply{}, fmt.Errorf("%s: %w", name, err)
	}
	status, text := splitReply(raw)
	r := reply{status: status, text: text}
	if status < 200 || status >= 300 {
		return r, fmt.Errorf("%s: the provider answered HTTP %d: %s", name, status, clip(text, 400))
	}
	if op.Read == "" || op.Read == "json" {
		if strings.Contains(text, "... [TRUNCATED") {
			return r, fmt.Errorf("%s: the reply was larger than the size limit and was cut off", name)
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(text)), &r.node); err != nil {
			return r, fmt.Errorf("%s: the reply is not JSON: %s", name, clip(text, 200))
		}
	}
	return r, nil
}

// command runs a command service's operation through System Hooks'
// sandboxed runner (core/flows): a file input as a file in the run's
// folder, the rest as text, the result read as the operation reads it.
func (t transport) command(ctx context.Context, name string, op Operation, vars map[string]any) (reply, error) {
	c := flows.Command{Steps: op.Run, Files: map[string]flows.CommandFile{}, Values: map[string]string{},
		Network: t.p.Network, Timeout: time.Duration(t.timeout(op)) * time.Second}
	for _, tok := range opTokens(op) {
		if f, err := fileVar(vars, tok); err == nil {
			data, err := os.ReadFile(f.Path)
			if err != nil {
				return reply{}, fmt.Errorf("%s: {%s}: %w", name, tok, err)
			}
			c.Files[tok] = flows.CommandFile{Data: data, Ext: filepath.Ext(f.Path)}
			continue
		}
		if v, ok := vars[tok]; ok && v != nil {
			c.Values[tok] = fmt.Sprint(v)
		}
	}
	res, err := flows.RunCommand(ctx, c)
	if err != nil {
		r := reply{text: res.Log}
		return r, fmt.Errorf("%s: %w", name, err)
	}
	r := reply{status: 200}
	if int64(len(res.Out)) > t.maxBytes() {
		return r, fmt.Errorf("%s: the result is larger than the %d MB limit", name, t.maxBytes()>>20)
	}
	switch op.Read {
	case "file":
		f, _, err := writeFile(bytes.NewReader(res.Out), t.maxBytes())
		r.file = f
		if err != nil {
			return r, fmt.Errorf("%s: %w", name, err)
		}
	case "text":
		r.text = string(res.Out)
	default:
		r.text = string(res.Out)
		if err := json.Unmarshal(res.Out, &r.node); err != nil {
			return r, fmt.Errorf("%s: what it printed is not JSON: %s", name, clip(r.text, 200))
		}
	}
	return r, nil
}

// fileVar is the file a variable holds: a file a step or the caller gave.
func fileVar(vars map[string]any, name string) (*File, error) {
	switch v := vars[name].(type) {
	case *File:
		if v != nil {
			return v, nil
		}
	case File:
		return &v, nil
	case string:
		if info, err := os.Stat(v); err == nil && info.Mode().IsRegular() {
			head := make([]byte, 512)
			if fh, err := os.Open(v); err == nil {
				n, _ := fh.Read(head)
				fh.Close()
				head = head[:n]
			}
			return &File{Path: v, Type: http.DetectContentType(head), Size: info.Size()}, nil
		}
	}
	return nil, fmt.Errorf("{%s} is not a file", name)
}

// multipartUpload builds the governed upload for a multipart send: its one
// file part, and the rest as text fields.
func multipartUpload(op Operation, vars map[string]any, max int64) (*FileUpload, io.Closer, error) {
	up := &FileUpload{Fields: map[string]string{}, ByteLimit: max}
	var closer io.Closer
	for part, v := range op.Body.(map[string]any) {
		if str, ok := v.(string); ok && wholeToken(str) {
			if f, err := fileVar(vars, tokenName(str)); err == nil {
				fh, err := os.Open(f.Path)
				if err != nil {
					return nil, nil, err
				}
				up.Reader, up.FieldName, up.FileName, closer = fh, part, filepath.Base(f.Path), fh
				continue
			}
		}
		rv := renderValue(v, vars)
		if rv == nil {
			continue // an optional input left out: no field, rather than "<nil>"
		}
		up.Fields[part] = fmt.Sprint(rv)
	}
	if up.Reader == nil {
		return nil, nil, fmt.Errorf("a multipart send has no file part with a file in it")
	}
	return up, closer, nil
}

// raw makes one request outside the dispatch's text reply: to the
// service's host through its credential's governed stream (or, keyless, a
// bounded client on that host), or, with no body, to a public address with
// no credential. A body never goes to another host.
func (t transport) raw(ctx context.Context, rawURL, method string, body io.Reader, ctype string) (*http.Response, func(int64), error) {
	timeout := time.Duration(t.timeout(Operation{})) * time.Second
	headers := map[string]string{}
	for k, v := range t.p.Headers {
		headers[k] = v
	}
	if ctype != "" {
		headers["Content-Type"] = ctype
	}
	switch {
	case t.sameHost(rawURL) && t.credential() != "":
		return Secure().OpenStream(t.sess, t.credential(), StreamRequest{URL: rawURL, Method: method, Headers: headers, Body: body, Timeout: timeout})
	case t.sameHost(rawURL):
		resp, err := doPlain(ctx, sourcehooks.NewBoundedHTTPClient(), rawURL, method, headers, body, timeout)
		return resp, nil, err
	case body != nil:
		return nil, nil, fmt.Errorf("a request with a body goes only to the service's own host, not %s", clip(rawURL, 120))
	}
	if err := RefuseNonPublicHost(rawURL); err != nil {
		return nil, nil, err
	}
	resp, err := doPlain(ctx, sourcehooks.NewPublicHTTPClient(), rawURL, method, nil, nil, timeout)
	return resp, nil, err
}

// readResponse reads a raw reply as the operation reads it, and closes it.
func (t transport) readResponse(name string, op Operation, resp *http.Response, done func(int64)) (reply, error) {
	var n int64
	defer func() {
		if done != nil {
			done(n)
		} else {
			resp.Body.Close()
		}
	}()
	r := reply{status: resp.StatusCode}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		n, r.text = int64(len(b)), string(b)
		return r, fmt.Errorf("%s: the provider answered HTTP %d: %s", name, resp.StatusCode, clip(r.text, 400))
	}
	if op.Read == "file" {
		f, written, err := writeFile(resp.Body, t.maxBytes())
		n, r.file = written, f
		if err != nil {
			return r, fmt.Errorf("%s: %w", name, err)
		}
		return r, nil
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	n, r.text = int64(len(b)), string(b)
	if err != nil {
		return r, fmt.Errorf("%s: %w", name, err)
	}
	if op.Read == "" || op.Read == "json" {
		if err := json.Unmarshal(b, &r.node); err != nil {
			return r, fmt.Errorf("%s: the reply is not JSON: %s", name, clip(r.text, 200))
		}
	}
	return r, nil
}

// splitReply reads the dispatch's "HTTP <status> ..." line off its reply.
func splitReply(s string) (int, string) {
	nl := strings.IndexByte(s, '\n')
	if nl < 0 {
		return 0, s
	}
	var status int
	fmt.Sscanf(s[:nl], "HTTP %d", &status)
	return status, s[nl+1:]
}

// address is the operation's URL with vars in it: escaped as path segments
// in a path, and query-escaped in a url, except a token that is the whole url
// (a result link a step saved), which goes in as it is.
func (t transport) address(op Operation, vars map[string]any) (string, error) {
	if op.URL != "" {
		if m := tokenRE.FindStringSubmatch(op.URL); m != nil && m[0] == strings.TrimSpace(op.URL) {
			u := fmt.Sprint(vars[m[1]])
			return resolveURL(t.p.BaseURL, u)
		}
		return resolveURL(t.p.BaseURL, fill(op.URL, vars, url.QueryEscape))
	}
	return strings.TrimRight(t.p.BaseURL, "/") + fill(op.Path, vars, url.PathEscape), nil
}

func resolveURL(base, ref string) (string, error) {
	b, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	r, err := url.Parse(strings.TrimSpace(ref))
	if err != nil {
		return "", fmt.Errorf("%q is not a URL", ref)
	}
	u := b.ResolveReference(r)
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("%q is not an http(s) address", ref)
	}
	return u.String(), nil
}

func fill(s string, vars map[string]any, esc func(string) string) string {
	return tokenRE.ReplaceAllStringFunc(s, func(tok string) string {
		v, ok := vars[tok[1:len(tok)-1]]
		if !ok || v == nil {
			return ""
		}
		return esc(fmt.Sprint(v))
	})
}

// renderRequest builds the body by the operation's send.
func renderRequest(op Operation, vars map[string]any) (string, string, error) {
	if op.Body == nil || op.Send == "none" {
		return "", "", nil
	}
	if op.Send == "form" {
		form := url.Values{}
		for k, v := range op.Body.(map[string]any) {
			if rv := renderValue(v, vars); rv != nil {
				form.Set(k, fmt.Sprint(rv))
			}
		}
		return form.Encode(), "application/x-www-form-urlencoded", nil
	}
	b, err := json.Marshal(renderValue(op.Body, vars))
	if err != nil {
		return "", "", err
	}
	return string(b), "application/json", nil
}

// renderValue fills a body template: a string that is exactly one token
// becomes the value itself, typed (a width stays a number); a token inside
// a longer string is written into it.
func renderValue(v any, vars map[string]any) any {
	switch t := v.(type) {
	case string:
		if m := tokenRE.FindStringSubmatch(t); m != nil && m[0] == t {
			if val, ok := vars[m[1]]; ok {
				return val
			}
			return nil
		}
		return fill(t, vars, func(s string) string { return s })
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, x := range t {
			out[k] = renderValue(x, vars)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = renderValue(x, vars)
		}
		return out
	}
	return v
}

// sameHost reports whether rawURL is on the service's own host.
func (t transport) sameHost(rawURL string) bool {
	a, err1 := url.Parse(t.p.BaseURL)
	b, err2 := url.Parse(rawURL)
	return err1 == nil && err2 == nil && strings.EqualFold(a.Host, b.Host) && a.Scheme == b.Scheme
}

// fetch reads a file at rawURL (a result link): on the service's host
// through its credential, anywhere else with none, through the public-only
// client.
func (t transport) fetch(ctx context.Context, rawURL, method string) (*File, int, error) {
	resp, done, err := t.raw(ctx, rawURL, method, nil, "")
	if err != nil {
		return nil, 0, fmt.Errorf("fetching %s: %w", clip(rawURL, 120), err)
	}
	r, err := t.readResponse("fetching "+clip(rawURL, 120), Operation{Read: "file"}, resp, done)
	return r.file, r.status, err
}

func doPlain(ctx context.Context, client *http.Client, rawURL, method string, headers map[string]string, body io.Reader, timeout time.Duration) (*http.Response, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		cancel()
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		cancel()
		return nil, err
	}
	resp.Body = cancelOnClose{resp.Body, cancel}
	return resp, nil
}

type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c cancelOnClose) Close() error { err := c.ReadCloser.Close(); c.cancel(); return err }

// filesDir is where the files a service call produced are written.
func filesDir() string { return ImageDir() }

// writeFile writes r to a new file as it arrives, refusing past max bytes,
// and names its type from its first bytes.
func writeFile(r io.Reader, max int64) (*File, int64, error) {
	dir := filesDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, 0, err
	}
	tmp, err := os.CreateTemp(dir, "service-*")
	if err != nil {
		return nil, 0, err
	}
	n, err := io.Copy(tmp, io.LimitReader(r, max+1))
	tmp.Close()
	if err == nil && n > max {
		err = fmt.Errorf("the file is larger than the %d MB limit", max>>20)
	}
	if err == nil && n == 0 {
		err = fmt.Errorf("the file is empty")
	}
	if err != nil {
		os.Remove(tmp.Name())
		return nil, n, err
	}
	head := make([]byte, 512)
	if f, err := os.Open(tmp.Name()); err == nil {
		m, _ := f.Read(head)
		head = head[:m]
		f.Close()
	}
	ctype := http.DetectContentType(head)
	final := tmp.Name() + extFor(ctype)
	if err := os.Rename(tmp.Name(), final); err != nil {
		final = tmp.Name()
	}
	return &File{Path: final, Type: ctype, Size: n}, n, nil
}

// writeBase64 decodes a base64 value (a data: URI's prefix dropped) into a
// file.
func writeBase64(s string, max int64) (*File, error) {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, ";base64,"); strings.HasPrefix(s, "data:") && i >= 0 {
		s = s[i+len(";base64,"):]
	}
	f, _, err := writeFile(base64.NewDecoder(base64.StdEncoding, strings.NewReader(s)), max)
	if err != nil {
		return nil, fmt.Errorf("the base64 value: %w", err)
	}
	return f, nil
}

func extFor(ctype string) string {
	switch {
	case strings.HasPrefix(ctype, "image/png"):
		return ".png"
	case strings.HasPrefix(ctype, "image/jpeg"):
		return ".jpg"
	case strings.HasPrefix(ctype, "image/gif"):
		return ".gif"
	case strings.HasPrefix(ctype, "image/webp"):
		return ".webp"
	case strings.HasPrefix(ctype, "audio/mpeg"):
		return ".mp3"
	case strings.HasPrefix(ctype, "audio/wave"):
		return ".wav"
	case strings.HasPrefix(ctype, "video/mp4"):
		return ".mp4"
	case strings.HasPrefix(ctype, "application/pdf"):
		return ".pdf"
	}
	return filepath.Ext("")
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "..."
	}
	return s
}
