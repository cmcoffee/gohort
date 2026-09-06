package temptool

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

func dispatchAPIModeTempTool(sess *ToolSession, tt *TempTool, args map[string]any) (string, error) {
	if sess.DB == nil {
		return "", fmt.Errorf("api tool %q requires a session with DB access", tt.Name)
	}
	// Network connector — API-mode is intrinsically network. Refuse
	// pre-dispatch when the connector blocks rather than letting the
	// HTTP layer fail with a confusing error. Same framework-floor
	// guarantee that gates web_search / fetch_url / the sandbox.
	if !sess.NetworkAllowed() {
		return "", fmt.Errorf("api tool %q refused: network is blocked for this turn (private mode is on)", tt.Name)
	}
	// Empty URL template = a broken DEFINITION, not a bad call. Without this,
	// the HTTP layer's bare "url is required" gave the model nothing to act
	// on — observed burning whole autonomous cycles retrying an action whose
	// stored url_template was empty, misreading it as a missing parameter.
	if strings.TrimSpace(tt.CommandTemplate) == "" {
		return "", fmt.Errorf("tool %q has an EMPTY url_template — its stored definition is broken, and NO arguments will make this call work. Do not retry. Fix the definition: tool_def(action=\"update\", name=%q, url_template=\"https://...\") — or for a toolbox action, actions=[{name:\"<action>\", url_template:\"https://...\"}]", tt.Name, tt.Name)
	}
	urlStr, err := substituteURL(tt.CommandTemplate, tt.Params, tt.Required, args)
	if err != nil {
		return "", fmt.Errorf("url template: %w", err)
	}
	method := strings.ToUpper(strings.TrimSpace(tt.Method))
	if method == "" {
		method = "GET"
	}

	// A declared upload takes over the body: multipart IS the request, so
	// BodyTemplate and ContentType do not apply. Everything the caller passed
	// other than the file itself rides along as plain form fields, which is how
	// an endpoint gets its model / response_format / subfolder alongside the
	// bytes without a template author writing any Go.
	if up := strings.TrimSpace(tt.UploadParam); up != "" {
		src, err := ResolveUploadSource(sess, StringArg(args, up))
		if err != nil {
			return "", fmt.Errorf("tool %q: %w", tt.Name, err)
		}
		fields := map[string]string{}
		for k, v := range args {
			if k == up || strings.HasPrefix(k, "__") {
				continue
			}
			if s := strings.TrimSpace(fmt.Sprint(v)); s != "" {
				fields[k] = s
			}
		}
		field := strings.TrimSpace(tt.UploadFormField)
		if field == "" {
			field = "file"
		}
		if method == "GET" {
			method = "POST" // an upload is never a GET; the default would 405
		}
		Debug("[temptool] api tool %q uploading %q (%d bytes) as field %q", tt.Name, src.Name, len(src.Data), field)
		return Secure().DispatchUpload(sess, tt.Credential, urlStr, method, FileUpload{
			Reader:    bytes.NewReader(src.Data),
			FieldName: field,
			FileName:  src.Name,
			Fields:    fields,
		})
	}

	var body string
	// A non-JSON ContentType (e.g. application/xml for CalDAV/SOAP) switches the
	// body to RAW substitution: placeholders insert verbatim and the body is NOT
	// JSON-validated. Empty/JSON content type keeps the JSON path (encode +
	// validate). This is what lets an XML/text API be a first-class api tool.
	rawBody := tt.ContentType != "" && !isJSONContentType(tt.ContentType)
	if tt.BodyTemplate != "" {
		if rawBody {
			body, err = substituteRaw(tt.BodyTemplate, tt.Params, tt.Required, args)
			if err != nil {
				return "", fmt.Errorf("body template: %w", err)
			}
			Debug("[temptool] api tool %q raw (%s) body (%d bytes)", tt.Name, tt.ContentType, len(body))
		} else {
			body, err = substituteJSON(tt.BodyTemplate, tt.Params, tt.Required, args)
			if err != nil {
				return "", fmt.Errorf("body template: %w", err)
			}
			// Validate the substituted body is valid JSON before sending.
			// Catches template-shape mistakes (mismatched braces, missing
			// commas, an LLM-baked literal that didn't escape correctly)
			// before the remote API rejects them with a generic "Expected
			// ',' or '}' at position N" — which is hard to act on without
			// seeing the actual body. The error returned to the LLM
			// includes the produced body so it can inspect what it built
			// and re-create the tool with a corrected template.
			var probe any
			if jerr := json.Unmarshal([]byte(body), &probe); jerr != nil {
				Debug("[temptool] api tool %q produced invalid JSON body: %s\nTEMPLATE: %s\nBODY: %s", tt.Name, jerr, tt.BodyTemplate, body)
				return "", fmt.Errorf("body template substitution produced invalid JSON: %w. Template: %s. Substituted body: %s. (For an XML/non-JSON API, set content_type — e.g. \"application/xml\" — so the body is sent RAW instead of being JSON-encoded/validated.)", jerr, tt.BodyTemplate, body)
			}
			Debug("[temptool] api tool %q body validated (%d bytes)", tt.Name, len(body))
		}
	}
	if sess.DB != nil && sess.Username != "" {
		TouchPersistentTempTool(sess.DB, sess.Username, tt.Name)
	}
	// When a response_pipe is configured, signal the dispatch layer to
	// read with a higher byte cap and skip the truncation marker —
	// the pipe will project the body down to a small output that fits
	// in context. Without this hint, large list-style endpoints get
	// cut mid-string and jq fails with "Unfinished string at EOF".
	var raw string
	if tt.Credential == "" {
		// Public-API path: no credential → plain HTTP. Same risk
		// profile as fetch_url (which the LLM can already call
		// directly). Used for public JSON endpoints (Reddit,
		// Wikipedia, public data feeds) where requiring a fake
		// credential just to satisfy the dispatcher would be silly.
		raw, err = dispatchPublicAPICall(urlStr, method, body, tt.ContentType, tt.Headers)
	} else {
		// Headers ride along: a protocol like CalDAV carries required
		// semantics in one (Depth: 1 on a REPORT/PROPFIND), and without
		// them the server answers 2xx with an empty result set.
		raw, err = Secure().DispatchToolCallRequest(sess, ToolCallRequest{
			Credential: tt.Credential, URL: urlStr, Method: method, Body: body,
			ContentType: tt.ContentType, Headers: tt.Headers,
			PipeFollowing: tt.ResponsePipe != "" || tt.ResponseExtract != nil,
		})
	}
	if err != nil {
		return raw, err
	}
	if tt.ResponsePipe == "" && tt.ResponseExtract == nil {
		return raw, nil
	}
	// The raw response from Secure().DispatchToolCall is shaped as:
	//   HTTP <code> <text>\n<body>
	// — the status line is there so the LLM can see HTTP errors when
	// reading the response directly. For the pipe path it's noise: jq
	// chokes on it, every pipe would need a `tail -n +2` prefix, and
	// running a filter against an error response (different shape than
	// a success body) usually produces garbage. Split the line off,
	// pipe only the body on 2xx, and skip the pipe entirely on non-2xx
	// so the LLM gets the unfiltered error to act on.
	statusLine, body := splitStatusLine(raw)
	if !isStatus2xx(statusLine) {
		return raw, nil
	}
	// response_extract: XML → JSON. Runs first on the 2xx body; if a
	// response_pipe is ALSO set, it then projects the extracted JSON (so
	// XML → JSON → jq is a valid chain). The model never writes XML parsing.
	if tt.ResponseExtract != nil {
		out, xerr := ExtractXML([]byte(body), *tt.ResponseExtract)
		if xerr != nil {
			hdr := strings.TrimSpace(statusLine)
			if hdr == "" {
				hdr = "HTTP 2xx"
			}
			return fmt.Sprintf("response_extract could not parse the response (%s): %v. The HTTP call SUCCEEDED — the body just didn't match the spec. Check select/fields against the real XML, or drop response_extract to see the raw body.", hdr, xerr), nil
		}
		body = string(out)
		if tt.ResponsePipe == "" {
			if statusLine != "" {
				return statusLine + "\n" + body, nil
			}
			return body, nil
		}
	}
	pipeCtx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	pres := RunSandboxedShellPipe(pipeCtx, tt.ResponsePipe, body)
	piped := strings.TrimSpace(pres.Output)
	if pres.TimedOut {
		notice := fmt.Sprintf("\n[response_pipe TIMED OUT after %s — command killed.]", commandTimeout)
		if piped == "" {
			return strings.TrimPrefix(notice, "\n"), nil
		}
		return piped + notice, nil
	}
	if pres.Err != nil {
		// Pipe failed (bad jq expression, missing binary, etc.). Surface
		// the error plus whatever output landed so the LLM can fix the
		// pipe via delete + recreate. Don't fall through to raw — the
		// whole point is to keep the raw response out of the LLM context.
		hint := ""
		// jq prints a red-herring "Unix shell quoting issues?" on a
		// compile error; the real culprit is almost always the `//`
		// alternative operator used bare in object construction, which
		// jq requires parenthesized. Say so directly.
		if strings.Contains(tt.ResponsePipe, "//") && strings.Contains(fmt.Sprint(pres.Err), "//") {
			hint = " HINT: jq requires the `//` alternative operator to be PARENTHESIZED inside object construction — write `{k: (.a // .b)}`, not `{k: .a // .b}`. Fix the response_pipe (delete + recreate the tool)."
		}
		if piped == "" {
			// The pipe produced nothing usable — but the HTTP call ALREADY
			// SUCCEEDED here (2xx; non-2xx returned raw above). Returning only
			// the pipe error HIDES that success: for a mutating call (POST) the
			// model reads "failed", retries, and double-submits — observed live,
			// a successful moltbook post reported as failed, retried into a 429,
			// then the model flailed reading the feed. Fall back to the raw body
			// (truncated) with an explicit do-not-retry directive so the model
			// sees the call worked and can read the result despite the broken
			// projection. The truncation bounds the context cost the pipe was
			// meant to avoid.
			rawBody := strings.TrimSpace(body)
			if len(rawBody) > maxOutput {
				rawBody = rawBody[:maxOutput] + "\n... [truncated]"
			}
			header := statusLine
			if header == "" {
				header = "HTTP 2xx"
			}
			return fmt.Sprintf("%s\n[response_pipe failed: %v — the HTTP call SUCCEEDED; showing the RAW response below. Do NOT retry the call (a repeat POST would double-submit). Fix the pipe later via delete + recreate. Pipe: %s]%s\n%s", header, pres.Err, tt.ResponsePipe, hint, rawBody), nil
		}
		return piped + fmt.Sprintf("\n[response_pipe exit: %v]%s", pres.Err, hint), nil
	}
	if len(piped) > maxOutput {
		totalLines := strings.Count(piped, "\n") + 1
		truncated := piped[:maxOutput]
		shown := strings.Count(truncated, "\n") + 1
		piped = truncated + fmt.Sprintf(
			"\n... [TRUNCATED: showing lines 1–%d of %d total (%d chars).]",
			shown, totalLines, len(piped))
	}
	// Re-prepend the status line so the LLM still sees the HTTP code.
	if statusLine != "" {
		return statusLine + "\n" + piped, nil
	}
	return piped, nil
}

// dispatchPublicAPICall makes a plain HTTP call for api-mode tools
// authored without a credential. Used for public APIs (Reddit JSON,
// Wikipedia, etc.) where requiring a registered credential just to
// satisfy the dispatcher would force an awkward pipeline+fetch_url
// indirection. Same risk profile as fetch_url which the LLM can
// already call directly.
//
// Returns the same "HTTP <code> <text>\n<body>" shape as
// Secure().DispatchToolCall so downstream response_pipe logic and
// status-line handling keep working unchanged.
func dispatchPublicAPICall(urlStr, method, body, contentType string, headers map[string]string) (string, error) {
	if method == "" {
		method = "GET"
	}
	req, err := http.NewRequest(method, urlStr, strings.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", publicAPIUserAgent)
	// Tool-declared headers (Depth, Accept, X-Api-Version, …). Auth headers
	// are refused on this path: an unauthenticated dispatch that carries a
	// hand-written credential is exactly what the credential machinery
	// exists to replace.
	for k, v := range headers {
		switch strings.ToLower(strings.TrimSpace(k)) {
		case "authorization", "proxy-authorization":
			continue
		}
		req.Header.Set(k, v)
	}
	if body != "" {
		ct := strings.TrimSpace(contentType)
		if ct == "" {
			ct = "application/json"
		}
		req.Header.Set("Content-Type", ct)
	}
	client := &http.Client{Timeout: publicAPITimeout}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("http %s %s: %w", method, urlStr, err)
	}
	defer resp.Body.Close()
	limited := io.LimitReader(resp.Body, publicAPIMaxResponseBytes)
	bodyBytes, readErr := io.ReadAll(limited)
	if readErr != nil {
		return "", fmt.Errorf("read response: %w", readErr)
	}
	statusLine := fmt.Sprintf("HTTP %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	out := statusLine + "\n" + string(bodyBytes)
	// Mark truncation explicitly when the body hit the cap so the LLM
	// can see whether it received the full payload.
	if int64(len(bodyBytes)) == publicAPIMaxResponseBytes {
		out += fmt.Sprintf("\n\n[response truncated at %d bytes]", publicAPIMaxResponseBytes)
	}
	return out, nil
}

const (
	publicAPITimeout          = 30 * time.Second
	publicAPIMaxResponseBytes = 1 * 1024 * 1024 // 1 MB
	publicAPIUserAgent        = "gohort-public-api-tool/1.0"
)

// splitStatusLine separates the leading "HTTP <code> <text>" line from
// the response body. Returns ("", raw) when the response doesn't start
// with an HTTP status line (defensive — should always match for output
// produced by Secure().dispatch).
func splitStatusLine(raw string) (status, body string) {
	if !strings.HasPrefix(raw, "HTTP ") {
		return "", raw
	}
	nl := strings.IndexByte(raw, '\n')
	if nl < 0 {
		return raw, ""
	}
	return raw[:nl], raw[nl+1:]
}

// isStatus2xx parses the numeric code out of an "HTTP <code> ..." line
// and reports whether it's in the 2xx range. Defensive — unparseable
// status lines fail closed (return false) so we don't pipe what looks
// like an error response.
func isStatus2xx(statusLine string) bool {
	if !strings.HasPrefix(statusLine, "HTTP ") {
		return false
	}
	rest := statusLine[len("HTTP "):]
	sp := strings.IndexByte(rest, ' ')
	if sp < 0 {
		sp = len(rest)
	}
	codeStr := rest[:sp]
	if len(codeStr) != 3 {
		return false
	}
	return codeStr[0] == '2'
}
