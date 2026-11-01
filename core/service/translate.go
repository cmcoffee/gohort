package service

// Reading an older rest_image connector as the two halves, so it can be
// fixed (and, once saved, become a service) with nothing stored rewritten
// until a person saves. The shapes data can carry translate: a synchronous
// submit, a poll whose result is at a path, and a result built into a URL
// and fetched. A ComfyUI workflow becomes a service running it with the comfyui
// builtin (comfyui.go). The ones a Go helper does for any other backend (an
// upload before it runs, the {images} token's inline base64) stay their own
// kind, and say so.

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// FromRestImage is c, a rest_image connector, as a service spec, with notes
// on what did not carry over.
func FromRestImage(c Connector) (Spec, []string, error) {
	var r RestImageSpec
	if err := json.Unmarshal(c.Spec, &r); err != nil {
		return Spec{}, nil, fmt.Errorf("its spec does not read: %w", err)
	}
	switch {
	case strings.TrimSpace(r.ComfyWorkflow) != "":
		return fromComfy(r)
	case strings.TrimSpace(r.UploadURL) != "" && r.SupportsImageInput():
		return Spec{}, nil, fmt.Errorf("it uploads a picture before it runs, which stays with the rest_image kind for now")
	case strings.Contains(r.SubmitBody, "{images}") || strings.Contains(r.SubmitBody, "{image}"):
		return Spec{}, nil, fmt.Errorf("it sends pictures inline as base64 ({images}), which stays with the rest_image kind for now")
	}
	sub, err := url.Parse(strings.TrimSpace(r.SubmitURL))
	if err != nil || sub.Host == "" {
		return Spec{}, nil, fmt.Errorf("its submit_url %q is not an address", r.SubmitURL)
	}
	base := sub.Scheme + "://" + sub.Host
	var notes []string
	p := Provider{Transport: "http", BaseURL: base, Operations: map[string]Operation{}}
	var config []ConfigField
	switch cred := strings.TrimSpace(r.Credential); cred {
	case "", "no_auth", "none":
		p.Auth.None = true
	default:
		// The key stays in the credential the backend sent it with; a
		// connection keeps its own, given under Settings on its row.
		send := "bearer"
		if c, ok := Secure().Load(cred); ok {
			switch c.Type {
			case SecureCredHeader:
				send = "header:" + c.ParamName
			case SecureCredQuery:
				send = "query:" + c.ParamName
			case SecureCredBasicAuth:
				send = "basic:" + c.Username
			}
		}
		config = append(config, ConfigField{Name: "api_key", Label: "API key", Type: "secret", Send: send, Required: true})
		p.Auth.Secret = "api_key"
	}
	body, err := templateBody(r.SubmitBody, r.PromptSuffix)
	if err != nil {
		return Spec{}, nil, err
	}
	p.Operations["submit"] = Operation{Method: firstNon(r.SubmitMethod, "POST"), Path: pathOf(sub), Body: body}
	b := Binding{Capability: "image.generate", Output: map[string]Output{}, Defaults: map[string]any{}}
	submit := Step{Call: "submit"}
	if r.PollURL == "" {
		b.Steps = []Step{submit}
		switch {
		case r.ImageB64Path != "":
			b.Output["image"] = Output{Path: r.ImageB64Path, As: "base64"}
		case r.ImageURLPath != "":
			b.Output["image"] = Output{Path: r.ImageURLPath, As: "url"}
		default:
			return Spec{}, nil, fmt.Errorf("it says neither where the image is (image_b64_path) nor its URL (image_url_path)")
		}
	} else {
		if r.SubmitIDPath != "" {
			submit.Save = map[string]string{"id": r.SubmitIDPath}
		}
		p.Operations["status"] = opAt(base, r.PollURL, firstNon(r.PollMethod, "GET"))
		wait := Step{Wait: "status", Until: r.PollReadyPath, EverySecs: r.PollIntervalSecs, MaxSecs: r.PollMaxSecs,
			Reason: r.PollErrorDetailPath}
		if r.PollErrorPath != "" && r.PollErrorValue != "" {
			wait.Fail = r.PollErrorPath + " == " + r.PollErrorValue
		}
		b.Steps = []Step{submit, wait}
		switch {
		case r.PollB64Path != "":
			b.Output["image"] = Output{Path: r.PollB64Path, As: "base64"}
		case r.PollURLPath != "":
			b.Output["image"] = Output{Path: r.PollURLPath, As: "url"}
		case r.PollURLTemplate != "":
			// The fields the URL is built from are saved off the finished
			// poll, and a last step fetches the picture.
			wait.Save = map[string]string{}
			for tok, path := range r.PollFields {
				wait.Save[tok] = path
			}
			b.Steps[1] = wait
			view := opAt(base, r.PollURLTemplate, "GET")
			view.Read = "file"
			p.Operations["view"] = view
			b.Steps = append(b.Steps, Step{Call: "view"})
			b.Output["image"] = Output{From: "body"}
		default:
			return Spec{}, nil, fmt.Errorf("it polls, but says nowhere where the finished image is")
		}
	}
	for k, v := range map[string]int{"width": r.DefaultWidth, "height": r.DefaultHeight, "steps": r.DefaultSteps} {
		if v > 0 {
			b.Defaults[k] = v
		}
	}
	b.Defaults["negative"] = r.DefaultNegative
	b.Defaults["seed"] = -1
	if r.DefaultModel != "" {
		b.Defaults["model"] = r.DefaultModel
	}
	if strings.TrimSpace(r.PromptGuidance) != "" {
		notes = append(notes, "its prompt guidance for agents is not carried: a service has no field for it yet")
	}
	s := Spec{Config: config, Provider: p, Provides: []Binding{b}}
	if len(config) > 0 {
		notes = append(notes, "its key stays in the credential "+strings.TrimSpace(r.Credential)+": a connection keeps its own, so give it under Settings on its row once saved")
	}
	if err := s.Validate(); err != nil {
		return Spec{}, notes, fmt.Errorf("translated, it would not be valid: %w", err)
	}
	return s, notes, nil
}

// templateBody reads a rest_image body template as JSON: its unquoted
// number tokens ({width}) quoted, so they parse and still render typed; the
// prompt suffix written after {prompt}.
func templateBody(tmpl, suffix string) (any, error) {
	tmpl = strings.TrimSpace(tmpl)
	if tmpl == "" {
		return nil, nil
	}
	var b strings.Builder
	for i := 0; i < len(tmpl); {
		loc := tokenRE.FindStringIndex(tmpl[i:])
		if loc == nil {
			b.WriteString(tmpl[i:])
			break
		}
		start, end := i+loc[0], i+loc[1]
		b.WriteString(tmpl[i:start])
		quoted := start > 0 && tmpl[start-1] == '"' && end < len(tmpl) && tmpl[end] == '"'
		inString := strings.Count(tmpl[:start], `"`)%2 == 1
		if !quoted && !inString {
			b.WriteString(`"` + tmpl[start:end] + `"`)
		} else {
			b.WriteString(tmpl[start:end])
		}
		i = end
	}
	var body any
	if err := json.Unmarshal([]byte(b.String()), &body); err != nil {
		return nil, fmt.Errorf("its submit_body is not a JSON template gohort can read: %w", err)
	}
	if suffix = strings.TrimSpace(suffix); suffix != "" {
		body = appendToPrompt(body, suffix)
	}
	return body, nil
}

func appendToPrompt(v any, suffix string) any {
	switch t := v.(type) {
	case string:
		if t == "{prompt}" {
			return "{prompt}, " + suffix
		}
	case map[string]any:
		for k, x := range t {
			t[k] = appendToPrompt(x, suffix)
		}
	case []any:
		for i, x := range t {
			t[i] = appendToPrompt(x, suffix)
		}
	}
	return v
}

// opAt is an operation at raw: a path when it is on base's host, else a
// whole url.
func opAt(base, raw, method string) Operation {
	if u, err := url.Parse(raw); err == nil && u.Host != "" && strings.EqualFold(u.Scheme+"://"+u.Host, base) {
		return Operation{Method: method, Path: pathOf(u)}
	}
	return Operation{Method: method, URL: raw}
}

func pathOf(u *url.URL) string {
	p := u.EscapedPath()
	// Tokens survive escaping as written.
	p = strings.NewReplacer("%7B", "{", "%7D", "}").Replace(p)
	if p == "" {
		p = "/"
	}
	if u.RawQuery != "" {
		p += "?" + strings.NewReplacer("%7B", "{", "%7D", "}").Replace(u.RawQuery)
	}
	return p
}

func firstNon(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

// fromComfy is rest_image ComfyUI backend r as the service keeping it
// (comfyui.go): the workflow runs as it did, and a key it sent with a
// credential goes the same way, given again under Settings.
func fromComfy(r RestImageSpec) (Spec, []string, error) {
	var notes []string
	send := ""
	switch cred := strings.TrimSpace(r.Credential); cred {
	case "", "no_auth", "none":
	default:
		send = credentialSend(cred)
		notes = append(notes, "its key stays in the credential "+cred+": a connection keeps its own, so give it under Settings on its row once saved")
	}
	s, err := comfyService(r, send)
	if err != nil {
		return Spec{}, notes, err
	}
	if err := s.Validate(); err != nil {
		return Spec{}, notes, fmt.Errorf("translated, it would not be valid: %w", err)
	}
	return s, notes, nil
}

// credentialSend is how a vault credential sent its key, as a secret
// setting's send.
func credentialSend(name string) string {
	c, ok := Secure().Load(name)
	if !ok {
		return "bearer"
	}
	switch c.Type {
	case SecureCredHeader:
		return "header:" + c.ParamName
	case SecureCredQuery:
		return "query:" + c.ParamName
	case SecureCredBasicAuth:
		return "basic:" + c.Username
	case SecureCredAWS:
		return "aws_sigv4"
	}
	return "bearer"
}
