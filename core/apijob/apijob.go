// Package apijob waits out an API that answers with a job instead of a
// result: submit, get an id, poll until the job is done (or failed), then read
// the result or fetch the file it produced.
//
// Song, video and image generators, transcription, report exports: the
// request starts the work and the answer comes minutes later. An api tool
// that just made the call returned "queued, id 8f3c" and left the model to
// improvise polling, or the author wrote a script that slept in a loop and hit
// the sandbox's time cap. With a job spec the tool does the waiting itself, as
// data: which field of the submit response is the id, where to ask how it is
// going, which field says done or failed, and where the finished thing is.
//
// The image connector's submit/poll/fetch engine (core/connector_restimage.go)
// is the same pattern specialized to pictures; this is the general one, so
// any api tool, and any template carrying one, can describe it without Go.
//
// A leaf package: the caller supplies the HTTP (Call), so credentials, the
// allowlist, private mode and audit stay where every other api call goes.
package apijob

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Spec describes one job-style API. Paths are dot-paths into a JSON response
// ("data.status", "items.0.url"), and {id} in a path or a URL is the job's id.
type Spec struct {
	// IDPath finds the job id in the submit response.
	IDPath string `json:"id_path"`
	// PollURL is asked until the job is done; absolute, or relative to the
	// submit URL.
	PollURL    string `json:"poll_url"`
	PollMethod string `json:"poll_method,omitempty"` // default GET
	// ReadyPath is the field that says the job is done: done once it holds
	// one of ReadyValues, or, with none given, once it is non-empty.
	ReadyPath   string   `json:"ready_path"`
	ReadyValues []string `json:"ready_values,omitempty"`
	// ErrorPath/ErrorValues say the job failed; ErrorDetailPath is where the
	// reason is.
	ErrorPath       string   `json:"error_path,omitempty"`
	ErrorValues     []string `json:"error_values,omitempty"`
	ErrorDetailPath string   `json:"error_detail_path,omitempty"`
	// ResultPath is the part of the final poll response the tool returns;
	// empty returns the whole response.
	ResultPath string `json:"result_path,omitempty"`
	// A file the job produced, fetched and delivered to the person: its URL
	// at FileURLPath, or built from FileURLTemplate with {name} tokens taken
	// from FileFields (name -> dot-path) and {id}. Relative URLs resolve
	// against the poll URL.
	FileURLPath     string            `json:"file_url_path,omitempty"`
	FileURLTemplate string            `json:"file_url_template,omitempty"`
	FileFields      map[string]string `json:"file_fields,omitempty"`
	// FileName names the delivered file ({id} allowed); empty takes the
	// URL's last segment.
	FileName string `json:"file_name,omitempty"`
	// IntervalSecs between polls (default 3); MaxSecs before giving up
	// (default 300); ExpectSecs is how long a job usually takes, which lets
	// a long one run in the background instead of holding the turn.
	IntervalSecs int `json:"interval_secs,omitempty"`
	MaxSecs      int `json:"max_secs,omitempty"`
	ExpectSecs   int `json:"expect_secs,omitempty"`
}

// Bounds.
const (
	DefaultInterval = 3
	MinInterval     = 1
	DefaultMax      = 300
	MaxMax          = 3600
)

// Parse reads a spec from a tool argument (an object, or JSON text), nil for
// none.
func Parse(v any) (*Spec, error) {
	if v == nil {
		return nil, nil
	}
	var raw []byte
	switch t := v.(type) {
	case string:
		if strings.TrimSpace(t) == "" {
			return nil, nil
		}
		raw = []byte(t)
	default:
		var err error
		if raw, err = json.Marshal(t); err != nil {
			return nil, err
		}
	}
	var s Spec
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("job is not a job spec: %v", err)
	}
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return &s, nil
}

// Validate refuses a spec that cannot run.
func (s Spec) Validate() error {
	switch {
	case strings.TrimSpace(s.PollURL) == "":
		return fmt.Errorf("job needs poll_url: where to ask how the job is going ({id} is the job's id)")
	case strings.TrimSpace(s.ReadyPath) == "":
		return fmt.Errorf("job needs ready_path: the field of the poll response that says it is done")
	case strings.Contains(s.PollURL, "{id}") && strings.TrimSpace(s.IDPath) == "":
		return fmt.Errorf("job needs id_path: poll_url uses {id}, so the submit response must say where the id is")
	case s.FileURLPath != "" && s.FileURLTemplate != "":
		return fmt.Errorf("job has both file_url_path and file_url_template: give one")
	case s.IntervalSecs < 0 || s.MaxSecs < 0 || s.MaxSecs > MaxMax:
		return fmt.Errorf("job max_secs must be at most %d", MaxMax)
	}
	for tok := range s.FileFields {
		if !strings.Contains(s.FileURLTemplate, "{"+tok+"}") {
			return fmt.Errorf("job file_fields names %q, which file_url_template does not use", tok)
		}
	}
	return nil
}

// Interval and Deadline are the spec's timing with defaults and bounds.
func (s Spec) Interval() time.Duration {
	n := s.IntervalSecs
	if n == 0 {
		n = DefaultInterval
	}
	if n < MinInterval {
		n = MinInterval
	}
	return time.Duration(n) * time.Second
}

func (s Spec) Deadline() time.Duration {
	n := s.MaxSecs
	if n == 0 {
		n = DefaultMax
	}
	if n > MaxMax {
		n = MaxMax
	}
	return time.Duration(n) * time.Second
}

// Response is one HTTP answer, as the caller got it.
type Response struct {
	Status int
	Body   []byte
}

// Call makes one poll request. The file a job produced is fetched by the
// caller afterwards, from Result.FileURL, since where its bytes go is the
// caller's business.
type Call func(ctx context.Context, method, rawURL string) (Response, error)

// Progress hears how the wait is going, once per poll.
type Progress func(elapsed time.Duration, polls int)

// Result is how the job ended.
type Result struct {
	ID       string
	Polls    int
	Elapsed  time.Duration
	Output   string // the result part of the final poll response
	FileURL  string // the file's URL, when the job produced one
	FileName string
}

// Run waits out a job whose submit response is submitBody (from submitURL),
// polling until it is done, failed, cancelled or out of time.
func Run(ctx context.Context, s Spec, submitURL string, submitBody []byte, call Call, progress Progress) (Result, error) {
	var res Result
	var submit any
	if err := json.Unmarshal(submitBody, &submit); err != nil {
		return res, fmt.Errorf("the submit response was not JSON, so there is no job to wait for: %s", clip(string(submitBody), 200))
	}
	if s.IDPath != "" {
		res.ID = Lookup(submit, s.IDPath, "")
		if res.ID == "" {
			return res, fmt.Errorf("the submit response has no job id at %q: %s", s.IDPath, clip(string(submitBody), 300))
		}
	}
	pollURL := resolve(submitURL, strings.ReplaceAll(s.PollURL, "{id}", url.PathEscape(res.ID)))
	method := strings.ToUpper(strings.TrimSpace(s.PollMethod))
	if method == "" {
		method = "GET"
	}
	start := time.Now()
	deadline := start.Add(s.Deadline())
	var node any
	for {
		if err := ctx.Err(); err != nil {
			return res, fmt.Errorf("stopped while waiting for job %s: %v", res.ID, err)
		}
		resp, err := call(ctx, method, pollURL)
		res.Polls++
		res.Elapsed = time.Since(start)
		if progress != nil {
			progress(res.Elapsed, res.Polls)
		}
		if err != nil {
			return res, fmt.Errorf("asking about job %s failed: %v", res.ID, err)
		}
		if resp.Status >= 400 {
			return res, fmt.Errorf("asking about job %s: HTTP %d: %s", res.ID, resp.Status, clip(string(resp.Body), 300))
		}
		node = nil
		if len(strings.TrimSpace(string(resp.Body))) > 0 {
			if err := json.Unmarshal(resp.Body, &node); err != nil {
				return res, fmt.Errorf("the poll response for job %s was not JSON: %s", res.ID, clip(string(resp.Body), 200))
			}
		}
		if s.ErrorPath != "" {
			if v := Lookup(node, s.ErrorPath, res.ID); v != "" && matches(v, s.ErrorValues) {
				detail := Lookup(node, s.ErrorDetailPath, res.ID)
				return res, fmt.Errorf("job %s failed (%s): %s", res.ID, v, chooseStr(detail, "no reason given"))
			}
		}
		if v := Lookup(node, s.ReadyPath, res.ID); v != "" && (len(s.ReadyValues) == 0 || matches(v, s.ReadyValues)) {
			break
		}
		if time.Now().Add(s.Interval()).After(deadline) {
			return res, fmt.Errorf("job %s was not done after %s (%d checks); it may still finish: ask %s later", res.ID, s.Deadline(), res.Polls, pollURL)
		}
		select {
		case <-ctx.Done():
			return res, fmt.Errorf("stopped while waiting for job %s: %v", res.ID, ctx.Err())
		case <-time.After(s.Interval()):
		}
	}
	if s.ResultPath != "" {
		res.Output = LookupJSON(node, s.ResultPath, res.ID)
	} else if node != nil {
		b, _ := json.Marshal(node)
		res.Output = string(b)
	}
	switch {
	case s.FileURLPath != "":
		res.FileURL = Lookup(node, s.FileURLPath, res.ID)
		if res.FileURL == "" {
			return res, fmt.Errorf("job %s is done but has no file URL at %q", res.ID, s.FileURLPath)
		}
	case s.FileURLTemplate != "":
		u := strings.ReplaceAll(s.FileURLTemplate, "{id}", url.QueryEscape(res.ID))
		for tok, p := range s.FileFields {
			v := Lookup(node, p, res.ID)
			if v == "" {
				return res, fmt.Errorf("job %s is done but %q (for {%s}) is empty", res.ID, p, tok)
			}
			u = strings.ReplaceAll(u, "{"+tok+"}", url.QueryEscape(v))
		}
		res.FileURL = u
	}
	if res.FileURL != "" {
		res.FileURL = resolve(pollURL, res.FileURL)
		res.FileName = fileName(s, res)
	}
	return res, nil
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// fileName names the delivered file: the spec's name, else the URL's last
// segment (or its filename query field), made safe for a workspace.
func fileName(s Spec, res Result) string {
	name := strings.ReplaceAll(strings.TrimSpace(s.FileName), "{id}", res.ID)
	if name == "" {
		if u, err := url.Parse(res.FileURL); err == nil {
			if f := u.Query().Get("filename"); f != "" {
				name = f
			} else {
				name = path.Base(u.Path)
			}
		}
	}
	name = strings.Trim(unsafeName.ReplaceAllString(name, "_"), "._")
	if name == "" || name == "/" {
		name = "job-" + unsafeName.ReplaceAllString(res.ID, "_")
	}
	return name
}

func resolve(base, ref string) string {
	b, err := url.Parse(base)
	if err != nil {
		return ref
	}
	r, err := url.Parse(ref)
	if err != nil {
		return ref
	}
	return b.ResolveReference(r).String()
}

func matches(v string, want []string) bool {
	if len(want) == 0 {
		return true
	}
	for _, w := range want {
		if strings.EqualFold(strings.TrimSpace(w), v) {
			return true
		}
	}
	return false
}

// walkPath follows a dot-path ({id} replaced) into decoded JSON.
func walkPath(node any, p, id string) (any, bool) {
	p = strings.TrimSpace(strings.ReplaceAll(p, "{id}", id))
	if p == "" {
		return node, true
	}
	cur := node
	for _, part := range strings.Split(p, ".") {
		switch t := cur.(type) {
		case map[string]any:
			v, ok := t[part]
			if !ok {
				return nil, false
			}
			cur = v
		case []any:
			i, err := strconv.Atoi(part)
			if err != nil || i < 0 || i >= len(t) {
				return nil, false
			}
			cur = t[i]
		default:
			return nil, false
		}
	}
	return cur, true
}

// Lookup reads a dot-path as text: a string as it is, a number or bool
// printed, "" when missing or null.
func Lookup(node any, p, id string) string {
	v, ok := walkPath(node, p, id)
	if !ok || v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// LookupJSON reads a dot-path as JSON text (a string unquoted).
func LookupJSON(node any, p, id string) string {
	v, ok := walkPath(node, p, id)
	if !ok || v == nil {
		return ""
	}
	if s, isStr := v.(string); isStr {
		return s
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "..."
	}
	return s
}

func chooseStr(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}
