// Package service is the unified connector: one record in two halves, how
// gohort reaches a service (the provider half) and what the service gives
// gohort (capabilities from a fixed list, each a contract with inputs, an
// output and the check it must pass). See docs/design/unified-connectors.md.
//
// A connector of Kind "service" is a spec of this shape. Everything that
// differs between two services offering the same capability is data here;
// the transports, the contracts and their checks, and the one wait engine
// (core/apijob) are the Go.
package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/cmcoffee/gohort/core/apijob"
)

// Kind is the connector kind of a service.
const Kind = "service"

// Spec is a service connector's payload.
type Spec struct {
	// Config are the settings the page using the connector holds and the
	// spec reads as {config.<name>} (config.go).
	Config   []ConfigField `json:"config,omitempty"`
	Provider Provider      `json:"provider"`
	Provides []Binding     `json:"provides"`
	// TestBudget is how many contract tests may run a day (default 5): a
	// test is a real call, and a real call can cost money.
	TestBudget int `json:"test_budget,omitempty"`

	// Set when the spec is settled for a call (config.go): the required
	// settings of each capability left unset, refused when that capability
	// is asked for; and how its calls carry the connection's secret.
	missing map[string][]string
	auth    *connectionAuth
}

// Provider is how gohort talks to the service.
type Provider struct {
	Transport  string               `json:"transport"`
	BaseURL    string               `json:"base_url"`
	Auth       Auth                 `json:"auth"`
	Headers    map[string]string    `json:"headers,omitempty"`
	Operations map[string]Operation `json:"operations"`
	Limits     *Limits              `json:"limits,omitempty"`
	Health     *Health              `json:"health,omitempty"`
	// Network lets a command service's program reach the network (a
	// downloader); off, it has none. The http transport ignores it.
	Network bool `json:"network,omitempty"`
}

// Auth names the secret setting the connection's calls carry (config.go),
// or says there is none (a service on the LAN). Credential, a credential in
// the vault, is how connectors carried a key before a connection kept its
// own. A secret's value never appears in a spec.
type Auth struct {
	Secret     string `json:"secret,omitempty"`
	Credential string `json:"credential,omitempty"`
	None       bool   `json:"none,omitempty"`
}

// Operation is one named request.
type Operation struct {
	Method string `json:"method,omitempty"`
	// Path is under the base URL; URL is a whole address instead.
	Path string `json:"path,omitempty"`
	URL  string `json:"url,omitempty"`
	// Send is how the request carries values: json (the default with a
	// body), form, multipart (body is its parts: one may be a file input),
	// file (body is one file input's token, sent as the whole body), or none.
	Send string `json:"send,omitempty"`
	Body any    `json:"body,omitempty"`
	// Read is what the reply is: json (the default), text, or file.
	Read        string `json:"read,omitempty"`
	TimeoutSecs int    `json:"timeout_secs,omitempty"`
	// Run, for the command transport, are the command lines in place of a
	// request: {token}s are inputs and saved values (a file input is a
	// file), {in} the line before's result, {out} or {out.png} the file a
	// line writes; with no {out}, what it prints is its result.
	Run []string `json:"run,omitempty"`
	// Builtin, in place of a request, names a built-in search
	// (builtin_search.go: pubmed, openalex, edgar): Go makes its requests,
	// to the provider's address through its credential, and gives back
	// {"results": [{title, url, snippet, published}]} for the spec to read.
	Builtin string `json:"builtin,omitempty"`
	// Options are what a builtin takes beyond a call's inputs: for comfyui
	// (comfyui.go), the workflow, its node map and how it runs.
	Options map[string]any `json:"options,omitempty"`
}

// Limits bound every transfer.
type Limits struct {
	TimeoutSecs int   `json:"timeout_secs,omitempty"`
	MaxBytes    int64 `json:"max_bytes,omitempty"`
}

// Health is the free check: an operation the provider does not charge for,
// and what its reply must hold.
type Health struct {
	Call      string `json:"call"`
	Expect    string `json:"expect,omitempty"`
	EveryMins int    `json:"every_mins,omitempty"`
}

// Binding is one capability the service gives gohort.
type Binding struct {
	Capability string `json:"capability"`
	// Config are the settings this capability takes of its own (its
	// model), beside the provider's (config.go).
	Config   []ConfigField     `json:"config,omitempty"`
	Steps    []Step            `json:"steps"`
	Output   map[string]Output `json:"output"`
	Defaults map[string]any    `json:"defaults,omitempty"`
	// Wide are inputs used over the defaults when gohort asks for a wide
	// picture (an article's header image): a size, or an aspect.
	Wide map[string]any `json:"wide,omitempty"`
	Cost *Cost          `json:"cost,omitempty"`
	// Research and Tool were how a search was offered to research and to
	// agents, before that moved to its tie-in, Admin > Research Sources. A
	// spec saved with them is read, so the migration can take them out; a
	// spec saving them is refused.
	Research *ResearchUse `json:"research,omitempty"`
	Tool     *ToolUse     `json:"tool,omitempty"`
	// Plain, for a search, takes a query's site: filters out before it is
	// sent: an API searching its own collection reads them as words.
	Plain bool `json:"plain,omitempty"`
	// Kind and About, for media.generate, say what it makes: Kind a short
	// name ("video", "3D model", "sound effects") that names its agent tool
	// (generate_<kind>_<connector>), About one line for the tool's
	// description. A blank kind is "media".
	Kind  string `json:"kind,omitempty"`
	About string `json:"about,omitempty"`
	// Pictures, for image.edit, is at most how many source pictures one
	// call takes: a number, or {config.<name>} naming a number setting (a
	// model's limit, set on the connector's row). Never more than the
	// picture inputs its operations use; absent, that many.
	Pictures any `json:"pictures,omitempty"`
}

// mediaKindRE is what a media.generate kind may be: a short name in words.
var mediaKindRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 -]{0,31}$`)

// MediaKind is binding b's kind as people read it: its kind, or "media".
func (b Binding) MediaKind() string {
	if k := strings.TrimSpace(b.Kind); k != "" {
		return k
	}
	return "media"
}

// ResearchUse is the research topics a search is asked on (legal, medical,
// financial and the like), or Always for every research run.
type ResearchUse struct {
	Topics []string `json:"topics,omitempty"`
	Always bool     `json:"always,omitempty"`
}

// ToolUse offers a search to agents as a tool: its name, and what tells an
// agent when to choose it over web_search.
type ToolUse struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

var searchToolNameRE = regexp.MustCompile(`^[a-z][a-z0-9_]{2,40}$`)

// Step is one call, or a wait on one.
type Step struct {
	Call      string            `json:"call,omitempty"`
	Save      map[string]string `json:"save,omitempty"`
	Wait      string            `json:"wait,omitempty"`
	Until     string            `json:"until,omitempty"`
	Fail      string            `json:"fail,omitempty"`
	Reason    string            `json:"reason,omitempty"`
	EverySecs int               `json:"every_secs,omitempty"`
	MaxSecs   int               `json:"max_secs,omitempty"`
}

// Output fills one contract output from the last reply: a path read as
// base64, url, text or json, or the reply itself (From "body") when the
// operation reads a file.
type Output struct {
	Path string `json:"path,omitempty"`
	As   string `json:"as,omitempty"`
	From string `json:"from,omitempty"`
	// Each reads a json output's list piece by piece: the path, within each
	// element of the list at Path, to the piece the output keeps (OpenAI's
	// embeddings: path "data", each "embedding").
	Each string `json:"each,omitempty"`
	// Fields reads a json output's list into objects of the contract's own
	// field names: each name, the path within every element it is read from
	// (a search's results: {"title": "title", "url": "link"}). A path may
	// name others to try when it has nothing, after a | ("page_age|age"). A
	// list the reply does not have reads as empty, as a search with no
	// results does.
	Fields map[string]string `json:"fields,omitempty"`
	// Single reads the value at a json output's path as one item, given as
	// a list holding it (a server that answers one embedding, not a list).
	Single bool `json:"single,omitempty"`
	// Or are other places the output may be, tried in turn when nothing is
	// at this one: for a server whose reply comes in more than one shape
	// (embeddings as data[].embedding, embeddings, or embedding).
	Or []Output `json:"or,omitempty"`
}

// Cost is what one call costs: flat, per unit the contract counts, or what
// the provider reports in its reply (preferred when present).
type Cost struct {
	PerCall   float64 `json:"per_call,omitempty"`
	PerUnit   float64 `json:"per_unit,omitempty"`
	Unit      string  `json:"unit,omitempty"`
	FromReply string  `json:"from_reply,omitempty"`
}

const (
	defaultTestBudget = 5
	defaultMaxBytes   = 50 << 20
	defaultTimeout    = 120
	minHealthMins     = 5
	defaultHealthMins = 30
)

// Parse reads a spec, refusing a field it does not know: a misspelled field
// read as absent is a setting silently not applied.
func Parse(raw []byte) (Spec, error) {
	var s Spec
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return s, fmt.Errorf("bad service spec: %w", err)
	}
	return s, nil
}

var tokenRE = regexp.MustCompile(`\{([a-z_][a-z0-9_]*)\}`)

// fileFieldRE is a file input written into a JSON body: {image.base64} is its
// bytes, base64-encoded, {image.type} its media type ("image/png"), and
// {image.dataurl} the two as one data: URL. An API that takes a picture
// inline in its JSON (Gemini's inlineData, OpenAI's image_url) is the case: a
// file send or a multipart part carries the file whole, and neither fits a
// JSON request. Only a whole string may be one, and it must name a file input.
var fileFieldRE = regexp.MustCompile(`\{([a-z_][a-z0-9_]*)\.(base64|type|dataurl)\}`)

// later says which stage brings a mode that is designed but not built, so a
// spec asking for it is refused with when, not accepted and ignored.
var later = map[string]string{
	"send:stream": "streams arrive with stage 5",
	"read:stream": "streams arrive with stage 5",
	"transport":   "only the http and command transports are built; mcp and peer arrive with stage 6",
}

// Validate refuses a spec that cannot run, or that names something it does
// not mean: an unknown capability, transport, mode, field or path.
func (s Spec) Validate() error {
	if err := s.validateConfig(); err != nil {
		return err
	}
	settled, err := s.settle(s.placeholders())
	if err != nil {
		return err
	}
	return settled.validateSettled()
}

// validateSettled checks a spec whose settings are filled in.
func (s Spec) validateSettled() error {
	p := s.Provider
	switch strings.TrimSpace(p.Transport) {
	case "http":
	case "command":
		return s.validateCommand()
	default:
		return fmt.Errorf("transport %q: %s", p.Transport, later["transport"])
	}
	if p.Network {
		return fmt.Errorf("network is for the command transport: an http service reaches its own address")
	}
	base, err := url.Parse(strings.TrimSpace(p.BaseURL))
	if err != nil || base.Host == "" || (base.Scheme != "http" && base.Scheme != "https") {
		return fmt.Errorf("base_url must be an http(s) address, got %q", p.BaseURL)
	}
	ways := 0
	for _, set := range []bool{strings.TrimSpace(p.Auth.Secret) != "", strings.TrimSpace(p.Auth.Credential) != "", p.Auth.None} {
		if set {
			ways++
		}
	}
	switch {
	case strings.TrimSpace(p.Auth.Credential) != "":
		return fmt.Errorf("auth names the credential %q, as connectors did before a connection kept its own key: add it again from its template, or declare a secret setting in config (type secret, send bearer, header:<name> or query:<name>) and name it in auth.secret", p.Auth.Credential)
	case ways > 1:
		return fmt.Errorf("auth gives more than one of secret and none: give one")
	case ways == 0:
		return fmt.Errorf("auth needs the secret its calls carry (a secret setting in config), or none: true for a service that takes no key")
	}
	for k := range p.Headers {
		switch strings.ToLower(strings.TrimSpace(k)) {
		case "authorization", "x-api-key", "api-key", "x-auth-token", "proxy-authorization":
			return fmt.Errorf("header %q carries auth, which comes from the credential: name it in auth", k)
		}
	}
	if len(p.Operations) == 0 {
		return fmt.Errorf("the provider has no operations")
	}
	for name, op := range p.Operations {
		if err := op.validate(name); err != nil {
			return err
		}
		if imageBuiltin(op.Builtin) {
			if err := validateImage(p.BaseURL, name, op); err != nil {
				return err
			}
		}
	}
	if l := p.Limits; l != nil && (l.TimeoutSecs < 0 || l.MaxBytes < 0) {
		return fmt.Errorf("limits must not be negative")
	}
	if h := p.Health; h != nil {
		if _, ok := p.Operations[h.Call]; !ok {
			return fmt.Errorf("health calls %q, which is not an operation", h.Call)
		}
		if _, err := parseCond(h.Expect); err != nil {
			return fmt.Errorf("health expect: %w", err)
		}
		if h.EveryMins != 0 && h.EveryMins < minHealthMins {
			return fmt.Errorf("health every_mins must be at least %d", minHealthMins)
		}
		if toks := opTokens(p.Operations[h.Call]); len(toks) > 0 {
			return fmt.Errorf("health calls %q, which needs {%s}: a health check has no inputs", h.Call, toks[0])
		}
	}
	return s.validateProvides()
}

// validateProvides checks the capability half, whatever the transport.
func (s Spec) validateProvides() error {
	p := s.Provider
	if len(s.Provides) == 0 {
		return fmt.Errorf("provides is empty: name at least one capability")
	}
	seen := map[string]bool{}
	for i, b := range s.Provides {
		if seen[b.Capability] {
			return fmt.Errorf("capability %q is provided twice", b.Capability)
		}
		seen[b.Capability] = true
		if err := b.validate(p); err != nil {
			return fmt.Errorf("provides[%d] (%s): %w", i, b.Capability, err)
		}
	}
	if s.TestBudget < 0 {
		return fmt.Errorf("test_budget must not be negative")
	}
	return nil
}

func (op Operation) validate(name string) error {
	if b := strings.TrimSpace(op.Builtin); imageBuiltin(b) {
		return nil // validateImage, with the provider's address
	} else if b != "" {
		if len(op.Options) > 0 {
			return fmt.Errorf("operation %q: a built-in search takes no options", name)
		}
		if _, ok := builtinSearches[b]; !ok {
			return fmt.Errorf("operation %q: %q is not a built-in search (there are %s)", name, b, strings.Join(builtinSearchNames(), ", "))
		}
		if op.Method != "" || op.Path != "" || op.URL != "" || op.Send != "" || op.Body != nil || op.Read != "" || len(op.Run) > 0 {
			return fmt.Errorf("operation %q names a built-in search, which makes its own requests: it has no method, path, url, send, body, read or run", name)
		}
		return nil
	}
	if len(op.Run) > 0 {
		return fmt.Errorf("operation %q has run lines, which are for the command transport", name)
	}
	switch strings.ToUpper(strings.TrimSpace(op.Method)) {
	case "", "GET", "POST", "PUT", "PATCH", "DELETE":
	default:
		return fmt.Errorf("operation %q: method %q is not one of GET, POST, PUT, PATCH, DELETE", name, op.Method)
	}
	switch {
	case op.Path != "" && op.URL != "":
		return fmt.Errorf("operation %q has both path and url: give one", name)
	case op.Path == "" && op.URL == "":
		return fmt.Errorf("operation %q needs a path (under base_url) or a url", name)
	case op.Path != "" && !strings.HasPrefix(op.Path, "/"):
		return fmt.Errorf("operation %q: path %q must start with /", name, op.Path)
	}
	switch op.Send {
	case "", "json", "form", "multipart", "file", "none":
	case "stream":
		return fmt.Errorf("operation %q: %s", name, later["send:"+op.Send])
	default:
		return fmt.Errorf("operation %q: send %q is not one of json, form, multipart, file, none", name, op.Send)
	}
	if op.Send == "none" && op.Body != nil {
		return fmt.Errorf("operation %q sends none but has a body", name)
	}
	if op.Send == "form" || op.Send == "multipart" {
		if _, ok := op.Body.(map[string]any); !ok {
			return fmt.Errorf("operation %q sends %s, so its body must be an object of fields", name, op.Send)
		}
	}
	if op.Send == "multipart" && op.Read == "file" {
		return fmt.Errorf("operation %q sends multipart and reads a file: not supported yet; read json or text", name)
	}
	if op.Send == "file" {
		if str, ok := op.Body.(string); !ok || !wholeToken(str) {
			return fmt.Errorf("operation %q sends a file, so its body must be one file input's token, like \"{image}\"", name)
		}
	}
	switch {
	case op.Read == "" || op.Read == "json" || op.Read == "text" || op.Read == "file":
	case strings.HasPrefix(op.Read, "stream"):
		return fmt.Errorf("operation %q: %s", name, later["read:stream"])
	default:
		return fmt.Errorf("operation %q: read %q is not one of json, text, file", name, op.Read)
	}
	if op.TimeoutSecs < 0 {
		return fmt.Errorf("operation %q: timeout_secs must not be negative", name)
	}
	return nil
}

func (b Binding) validate(p Provider) error {
	c, ok := Lookup(b.Capability)
	if !ok {
		return fmt.Errorf("unknown capability %q (known: %s)", b.Capability, strings.Join(Names(), ", "))
	}
	if (b.Kind != "" || b.About != "") && b.Capability != "media.generate" {
		return fmt.Errorf("kind and about say what a media.generate makes: media.generate only")
	}
	if b.Kind != "" && !mediaKindRE.MatchString(strings.TrimSpace(b.Kind)) {
		return fmt.Errorf("kind %q: a short name in words, up to 32 letters, digits, spaces or dashes (video, 3D model, sound effects)", b.Kind)
	}
	if len(b.About) > 200 {
		return fmt.Errorf("about is one line: up to 200 characters")
	}
	if b.Pictures != nil {
		if b.Capability != "image.edit" {
			return fmt.Errorf("pictures is how many pictures an edit takes: image.edit only")
		}
		switch v := b.Pictures.(type) {
		case float64:
			if v < 1 || v != float64(int(v)) {
				return fmt.Errorf("pictures is a whole number, at least 1")
			}
		case string:
			if m := configRE.FindStringSubmatch(v); m == nil || m[0] != v {
				return fmt.Errorf("pictures is a number, or one {config.<name>} naming a number setting")
			}
		default:
			return fmt.Errorf("pictures is a number, or one {config.<name>} naming a number setting")
		}
	}
	if b.Plain && b.Capability != "web.search" && b.Capability != "image.search" {
		return fmt.Errorf("plain takes site: filters out of a search's query: web.search and image.search only")
	}
	if b.Research != nil || b.Tool != nil {
		return fmt.Errorf("research and tool are set where a search is referenced, under Admin > Research Sources, not in its spec")
	}
	if len(b.Steps) == 0 {
		return fmt.Errorf("no steps")
	}
	// What each step may use: the contract's inputs, then what earlier steps
	// saved.
	have := map[string]bool{}
	for _, in := range c.Inputs {
		have[in.Name] = true
	}
	var last Operation
	for i, st := range b.Steps {
		switch {
		case st.Call != "" && st.Wait != "":
			return fmt.Errorf("step %d both calls and waits: give one", i+1)
		case st.Call == "" && st.Wait == "":
			return fmt.Errorf("step %d neither calls nor waits", i+1)
		}
		name := st.Call + st.Wait
		op, ok := p.Operations[name]
		if !ok {
			return fmt.Errorf("step %d names %q, which is not an operation", i+1, name)
		}
		switch {
		case imageBuiltin(op.Builtin) && b.Capability != "image.generate" && b.Capability != "image.edit":
			return fmt.Errorf("step %d calls %q, an image backend: it gives image.generate or image.edit", i+1, name)
		case op.Builtin != "" && !imageBuiltin(op.Builtin) && b.Capability != "web.search":
			return fmt.Errorf("step %d calls %q, a built-in search: it gives web.search's results", i+1, name)
		}
		for _, tok := range opTokens(op) {
			if !have[tok] {
				return fmt.Errorf("step %d calls %q, which uses {%s}: no input or earlier step gives it", i+1, name, tok)
			}
		}
		if err := c.checkFileSend(name, op); err != nil {
			return fmt.Errorf("step %d: %w", i+1, err)
		}
		for _, p := range st.paths() {
			if err := pathTokensKnown(p, have); err != nil {
				return fmt.Errorf("step %d: %w", i+1, err)
			}
		}
		if st.Wait != "" {
			if strings.TrimSpace(st.Until) == "" {
				return fmt.Errorf("step %d waits with no until: say what done looks like", i+1)
			}
			for _, cond := range []string{st.Until, st.Fail} {
				if _, err := parseCond(cond); err != nil {
					return fmt.Errorf("step %d: %w", i+1, err)
				}
			}
			if err := apijob.ValidPath(st.Reason); err != nil {
				return fmt.Errorf("step %d reason: %w", i+1, err)
			}
			if st.EverySecs < 0 || st.MaxSecs < 0 || st.MaxSecs > apijob.MaxMax {
				return fmt.Errorf("step %d: max_secs must be at most %d", i+1, apijob.MaxMax)
			}
		}
		for v, path := range st.Save {
			if !tokenRE.MatchString("{" + v + "}") {
				return fmt.Errorf("step %d saves %q, which is not a variable name", i+1, v)
			}
			for _, p := range strings.Split(path, "|") {
				if err := apijob.ValidPath(p); err != nil || strings.TrimSpace(p) == "" {
					if err == nil {
						err = fmt.Errorf("path %q has an empty alternative", path)
					}
					return fmt.Errorf("step %d save %s: %w", i+1, v, err)
				}
			}
			have[v] = true
		}
		last = op
	}
	for _, out := range b.Output {
		if err := pathTokensKnown(out.Path, have); err != nil {
			return err
		}
	}
	if b.Cost != nil {
		if err := pathTokensKnown(b.Cost.FromReply, have); err != nil {
			return err
		}
	}
	for name, out := range b.Output {
		spec, ok := c.output(name)
		if !ok {
			return fmt.Errorf("output %q is not one of %s's outputs", name, c.Name)
		}
		if err := out.validate(name, spec, last); err != nil {
			return err
		}
	}
	for _, o := range c.Outputs {
		if _, ok := b.Output[o.Name]; !ok && o.Required {
			return fmt.Errorf("output %q is required: say where it comes from", o.Name)
		}
	}
	for k := range b.Defaults {
		if _, ok := c.input(k); !ok {
			return fmt.Errorf("default %q is not one of %s's inputs", k, c.Name)
		}
	}
	for k := range b.Wide {
		if _, ok := c.input(k); !ok {
			return fmt.Errorf("wide %q is not one of %s's inputs", k, c.Name)
		}
	}
	if cost := b.Cost; cost != nil {
		if cost.PerCall < 0 || cost.PerUnit < 0 {
			return fmt.Errorf("cost must not be negative")
		}
		if cost.PerUnit > 0 && !contains(c.Units, cost.Unit) {
			return fmt.Errorf("cost unit %q is not one %s counts (%s)", cost.Unit, c.Name, strings.Join(c.Units, ", "))
		}
		if err := apijob.ValidPath(cost.FromReply); err != nil {
			return fmt.Errorf("cost from_reply: %w", err)
		}
	}
	return nil
}

func (o Output) validate(name string, spec Port, last Operation) error {
	for i, alt := range o.Or {
		if len(alt.Or) > 0 {
			return fmt.Errorf("output %q: or %d has an or of its own: list every place in the one or", name, i+1)
		}
		if err := alt.validate(name, spec, last); err != nil {
			return fmt.Errorf("or %d: %w", i+1, err)
		}
	}
	if o.From != "" {
		if o.From != "body" || o.Path != "" || o.As != "" || o.Each != "" || len(o.Fields) > 0 || o.Single {
			return fmt.Errorf("output %q: from must be \"body\", alone", name)
		}
		if spec.Type != TypeFile || (last.Read != "file" && !imageBuiltin(last.Builtin)) {
			return fmt.Errorf("output %q: from body needs a file output and a last operation that reads file", name)
		}
		return nil
	}
	// A list read into a json output may be the whole reply (an API that
	// answers with a bare list): its path is then empty.
	readsList := o.Each != "" || len(o.Fields) > 0 || o.Single
	if strings.TrimSpace(o.Path) == "" && !readsList {
		return fmt.Errorf("output %q needs a path, or from: body", name)
	}
	if strings.TrimSpace(o.Path) != "" {
		if err := apijob.ValidPath(o.Path); err != nil {
			return fmt.Errorf("output %q: %w", name, err)
		}
	}
	allowed := map[ValueType][]string{
		TypeFile:   {"base64", "url"},
		TypeText:   {"text", ""},
		TypeNumber: {"text", ""},
		TypeJSON:   {"json", ""},
	}[spec.Type]
	if !contains(allowed, o.As) {
		return fmt.Errorf("output %q is a %s: as must be one of %s", name, spec.Type, strings.Join(allowed, ", "))
	}
	if o.Each != "" {
		if spec.Type != TypeJSON {
			return fmt.Errorf("output %q: each reads a list, for a json output", name)
		}
		if err := apijob.ValidPath(o.Each); err != nil {
			return fmt.Errorf("output %q each: %w", name, err)
		}
	}
	if o.Single && (spec.Type != TypeJSON || o.Each != "" || len(o.Fields) > 0) {
		return fmt.Errorf("output %q: single reads one item as a list, for a json output, without each or fields", name)
	}
	if len(o.Fields) > 0 {
		if spec.Type != TypeJSON || o.Each != "" {
			return fmt.Errorf("output %q: fields reads a list into objects, for a json output, without each", name)
		}
		for f, paths := range o.Fields {
			for _, path := range strings.Split(paths, "|") {
				if err := apijob.ValidPath(path); err != nil || strings.TrimSpace(path) == "" {
					return fmt.Errorf("output %q field %s: say the path within each element it is read from", name, f)
				}
			}
		}
	}
	return nil
}

// validateCommand checks a command service: its operations are run lines,
// with no address, credential or headers, since a local program has none.
func (s Spec) validateCommand() error {
	p := s.Provider
	switch {
	case strings.TrimSpace(p.BaseURL) != "":
		return fmt.Errorf("a command service has no base_url: its operations are run lines")
	case p.Auth.Secret != "" || p.Auth.Credential != "" || p.Auth.None:
		return fmt.Errorf("a command service takes no auth: a local program is given no key")
	case len(p.Headers) > 0:
		return fmt.Errorf("a command service has no headers")
	case len(p.Operations) == 0:
		return fmt.Errorf("the provider has no operations")
	}
	for name, op := range p.Operations {
		if len(op.Run) == 0 {
			return fmt.Errorf("operation %q needs run: the command lines it runs", name)
		}
		if op.Path != "" || op.URL != "" || op.Method != "" || op.Send != "" || op.Body != nil {
			return fmt.Errorf("operation %q is a command: it has run lines, not a path, url, method, send or body", name)
		}
		switch op.Read {
		case "", "json", "text", "file":
		default:
			return fmt.Errorf("operation %q: read %q is not one of json, text, file", name, op.Read)
		}
		for _, line := range op.Run {
			if strings.TrimSpace(line) == "" {
				return fmt.Errorf("operation %q has an empty run line", name)
			}
			if strings.Contains(line, "{outdir}") {
				return fmt.Errorf("operation %q: {outdir} (several results) is not supported for a service", name)
			}
		}
		if op.TimeoutSecs < 0 {
			return fmt.Errorf("operation %q: timeout_secs must not be negative", name)
		}
	}
	if l := p.Limits; l != nil && (l.TimeoutSecs < 0 || l.MaxBytes < 0) {
		return fmt.Errorf("limits must not be negative")
	}
	if h := p.Health; h != nil {
		if _, ok := p.Operations[h.Call]; !ok {
			return fmt.Errorf("health calls %q, which is not an operation", h.Call)
		}
		if _, err := parseCond(h.Expect); err != nil {
			return fmt.Errorf("health expect: %w", err)
		}
		if h.EveryMins != 0 && h.EveryMins < minHealthMins {
			return fmt.Errorf("health every_mins must be at least %d", minHealthMins)
		}
		if toks := opTokens(p.Operations[h.Call]); len(toks) > 0 {
			return fmt.Errorf("health calls %q, which needs {%s}: a health check has no inputs", h.Call, toks[0])
		}
	}
	return s.validateProvides()
}

// paths are the paths a step reads its reply with.
func (st Step) paths() []string {
	out := []string{st.Reason}
	for _, p := range st.Save {
		for _, alt := range strings.Split(p, "|") {
			out = append(out, strings.TrimSpace(alt))
		}
	}
	for _, cond := range []string{st.Until, st.Fail} {
		if c, _ := parseCond(cond); c != nil {
			out = append(out, c.path)
		}
	}
	return out
}

// pathTokensKnown refuses a path that names a variable nothing gives
// ("outputs.{job}.images" with no job saved).
func pathTokensKnown(p string, have map[string]bool) error {
	for _, m := range tokenRE.FindAllStringSubmatch(p, -1) {
		if !have[m[1]] {
			return fmt.Errorf("path %q uses {%s}: no input or earlier step gives it", p, m[1])
		}
	}
	return nil
}

// checkFileSend refuses a file where the contract has none: a file send's
// token, and a multipart's file part, must name one of the contract's file
// inputs, and a multipart carries one file part.
func (c Contract) checkFileSend(name string, op Operation) error {
	isFile := func(tok string) bool {
		p, ok := c.input(tok)
		return ok && p.Type == TypeFile
	}
	if op.Send == "" || op.Send == "json" || op.Send == "form" {
		if err := checkFileFields(name, op.Body, isFile); err != nil {
			return err
		}
	}
	switch op.Send {
	case "file":
		if tok := tokenName(op.Body.(string)); !isFile(tok) {
			return fmt.Errorf("operation %q sends {%s} as a file, which is not one of %s's file inputs", name, tok, c.Name)
		}
	case "multipart":
		files := 0
		for part, v := range op.Body.(map[string]any) {
			str, _ := v.(string)
			if wholeToken(str) && isFile(tokenName(str)) {
				files++
				continue
			}
			for _, m := range tokenRE.FindAllStringSubmatch(fmt.Sprint(v), -1) {
				if isFile(m[1]) {
					return fmt.Errorf("operation %q: part %q writes the file {%s} into text; a file part is the token alone", name, part, m[1])
				}
			}
		}
		if files > 1 {
			return fmt.Errorf("operation %q has %d file parts: one per request for now", name, files)
		}
	}
	return nil
}

// checkFileFields refuses a {name.base64}, {name.type} or {name.dataurl} that
// is not a whole string, or that names something other than one of the
// contract's file inputs.
func checkFileFields(name string, v any, isFile func(string) bool) error {
	switch t := v.(type) {
	case string:
		for _, m := range fileFieldRE.FindAllStringSubmatch(t, -1) {
			if m[0] != strings.TrimSpace(t) {
				return fmt.Errorf("operation %q writes {%s.%s} into text; a file field is the token alone", name, m[1], m[2])
			}
			if !isFile(m[1]) {
				return fmt.Errorf("operation %q writes {%s.%s}, and %s is not one of the contract's file inputs", name, m[1], m[2], m[1])
			}
		}
	case map[string]any:
		for _, x := range t {
			if err := checkFileFields(name, x, isFile); err != nil {
				return err
			}
		}
	case []any:
		for _, x := range t {
			if err := checkFileFields(name, x, isFile); err != nil {
				return err
			}
		}
	}
	return nil
}

func wholeToken(s string) bool {
	m := tokenRE.FindStringSubmatch(strings.TrimSpace(s))
	return m != nil && m[0] == strings.TrimSpace(s)
}

func tokenName(s string) string {
	if m := tokenRE.FindStringSubmatch(strings.TrimSpace(s)); m != nil {
		return m[1]
	}
	return ""
}

// opTokens are the {tokens} an operation uses, in its address and body.
func opTokens(op Operation) []string {
	text := op.Path + op.URL + strings.Join(op.Run, "\n")
	if op.Body != nil {
		b, _ := json.Marshal(op.Body)
		text += string(b)
	}
	set := map[string]bool{}
	for _, m := range tokenRE.FindAllStringSubmatch(text, -1) {
		if len(op.Run) > 0 && (m[1] == "in" || m[1] == "out") {
			continue // a command's own: the line before's result, and its own
		}
		set[m[1]] = true
	}
	for _, m := range fileFieldRE.FindAllStringSubmatch(text, -1) {
		set[m[1]] = true
	}
	out := make([]string, 0, len(set))
	for t := range set {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
