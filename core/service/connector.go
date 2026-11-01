package service

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"

	. "github.com/cmcoffee/gohort/core"
)

func init() { RegisterConnectorKind(Kind, handler{}) }

// handler is the service connector kind. A service adds new outside reach,
// so it is not auto-approved: an admin approves it, as for rest_image.
type handler struct{}

func specOf(c Connector) (Spec, error) {
	s, err := Parse(c.Spec)
	if err != nil {
		return s, err
	}
	return s, s.Validate()
}

func (handler) Validate(c Connector) error {
	_, err := specOf(c)
	return err
}

// ImportSettings is what an imported connection keeps of the settings it
// brought: each its spec declares and takes, by the check a page's save
// makes. Any other is left out with the reason, never kept to fail a call.
func (handler) ImportSettings(c Connector, values map[string]string) (map[string]string, []string) {
	s, err := Parse(c.Spec)
	if err != nil {
		return nil, []string{"its settings were left out: its spec does not read"}
	}
	declared := map[string]Setting{}
	for _, st := range s.settings() {
		declared[st.Key] = st
	}
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	keep := map[string]string{}
	var dropped []string
	for _, k := range keys {
		st, ok := declared[k]
		switch {
		case !ok || st.Field.IsSecret():
			dropped = append(dropped, fmt.Sprintf("its setting %q was left out: its spec takes no such setting", k))
		case checkValue(st.Field, values[k]) != nil:
			dropped = append(dropped, fmt.Sprintf("its %s was left out: %v", st.Field.Title(), checkValue(st.Field, values[k])))
		default:
			keep[k] = values[k]
		}
	}
	return keep, dropped
}

var (
	ownedMu sync.Mutex
	owned   = map[string]bool{} // image backends this kind registered
)

// Materialize puts each capability where gohort looks for it: an
// image.generate service becomes an image backend named for the connector,
// usable everywhere a built-in provider is. The spec is read live on every
// call, so an edit takes effect without registering again.
func (handler) Materialize(c Connector) error {
	s, err := specOf(c)
	if err != nil {
		return err
	}
	name := strings.TrimSpace(c.Name)
	// A ComfyUI workflow is served where a rest_image backend is, by the
	// machinery that runs one: generate, edit, blend and its tool alike.
	_, comfy := comfyOperation(s)
	if comfy {
		unregisterOwned(name)
		c.ServeImageBackend()
	} else if _, _, err := s.binding("image.generate"); err == nil {
		RegisterImageBackend(name, func(ctx context.Context, prompt string, landscape bool) (*ImageGenResult, error) {
			return generateImage(ctx, name, prompt, landscape)
		})
		ownedMu.Lock()
		owned[name] = true
		ownedMu.Unlock()
	} else {
		unregisterOwned(name)
	}
	if _, _, err := s.binding("image.edit"); err == nil && !comfy {
		RegisterImageEditBackend(name, func(sess *ToolSession, prompt string, image []byte) (*ImageGenResult, error) {
			return editImage(sess, name, prompt, image)
		})
	} else {
		RegisterImageEditBackend(name, nil)
	}
	if _, _, err := s.binding("music.generate"); err == nil {
		registerMusicTool(name)
	}
	if s.Provider.Health != nil {
		startHealth()
	}
	return nil
}

func (handler) Teardown(c Connector) error {
	name := strings.TrimSpace(c.Name)
	ownedMu.Lock()
	mine := owned[name]
	delete(owned, name)
	ownedMu.Unlock()
	if mine {
		UnregisterImageBackend(name)
	}
	RegisterImageEditBackend(name, nil)
	if s, err := Parse(c.Spec); err == nil {
		if _, comfy := comfyOperation(s); comfy {
			c.StopImageBackend()
		}
	}
	return nil
}

// unregisterOwned drops the image backend this kind registered
// for name, when a re-save no longer provides image.generate.
func unregisterOwned(name string) {
	ownedMu.Lock()
	mine := owned[name]
	delete(owned, name)
	ownedMu.Unlock()
	if mine {
		UnregisterImageBackend(name)
	}
}

func (handler) Summary(c Connector) string {
	s, err := Parse(c.Spec)
	if err != nil {
		return "service (unreadable spec)"
	}
	// Read with what the connection keeps, a stand-in for what it does not
	// yet, so its row names its address rather than a token.
	values := s.placeholders()
	for _, st := range s.settings() {
		if st.Field.Kind() == "url" && st.Field.Default == "" {
			values[st.Key] = "https://not-set-yet"
		}
	}
	for k, v := range ConnectionConfig(RootDB, c) {
		values[k] = v
	}
	if settled, err := s.settle(values); err == nil {
		s = settled
	}
	return s.Describe(c.Name)
}

// Describe is the service as the wrapper it is, read inside to outside:
// what gohort asks of it (each capability, in the admin's words), the
// mapping that turns that into calls, and what it reaches, with what
// reaching it takes (docs/integrations.md). A program on this machine is
// reached like an API: what it runs, and whether it has the network, are the
// first things an admin approving it needs to see.
//
// One line stays one line: a service giving several capabilities names them
// and counts its endpoints, and Endpoints says how each is used.
func (s Spec) Describe(name string) string {
	var inside, gave []string
	for _, b := range s.Provides {
		c, _ := Lookup(b.Capability)
		gives := c.Gives
		switch {
		case b.Capability == "music.generate":
			gives = "agent tool " + (&musicTool{connector: name}).Name()
		case gives == "":
			gives = b.Capability
		}
		mapping := mappingText(b)
		if b.Dialect != "" {
			mapping = b.Dialect + " dialect"
			if m := b.Defaults["model"]; m != nil {
				mapping += fmt.Sprintf(" (model %v)", m)
			}
		}
		inside = append(inside, gives+" ⇐ "+mapping)
		gave = append(gave, gives)
	}
	line, count := strings.Join(inside, "; "), ""
	if len(s.Provides) > 1 {
		line = strings.Join(gave, ", ")
		count = fmt.Sprintf(", %d endpoints", len(s.Provider.Operations))
	}
	if s.Provider.Transport == "command" {
		progs := map[string]bool{}
		var list []string
		for _, op := range s.Provider.Operations {
			for _, run := range op.Run {
				if p := strings.Fields(run); len(p) > 0 && !progs[p[0]] {
					progs[p[0]] = true
					list = append(list, p[0])
				}
			}
		}
		sort.Strings(list)
		net := "no network"
		if s.Provider.Network {
			net = "with network"
		}
		return fmt.Sprintf("%s ⇐ %s on this machine (%s)%s", line, strings.Join(list, ", "), net, count)
	}
	host := s.Provider.BaseURL
	if u, err := url.Parse(host); err == nil && u.Host != "" {
		host = u.Host
	}
	auth := "no key"
	if f, ok := s.secretField(); ok {
		auth = "its " + f.Title()
		if strings.HasPrefix(f.Send, "aws_sigv4") {
			auth = "signed by its " + f.Title() + ", or as this machine"
		}
	}
	return fmt.Sprintf("%s ⇐ %s (%s)%s", line, host, auth, count)
}

// mappingText is a capability's mapping onto the outside: the endpoints it
// calls, and the ones it waits on, in order.
func mappingText(b Binding) string {
	var steps []string
	for _, st := range b.Steps {
		if st.Call != "" {
			steps = append(steps, st.Call)
		} else {
			steps = append(steps, "wait on "+st.Wait)
		}
	}
	return strings.Join(steps, ", then ")
}

// Provides reports whether connector c is a service giving capability.
func Provides(c Connector, capability string) bool {
	if c.Kind != Kind {
		return false
	}
	s, err := Parse(c.Spec)
	if err != nil {
		return false
	}
	_, _, err = s.binding(capability)
	return err == nil
}

// Default is the value c's binding for capability gives input when the caller
// gives none ("" when it gives none either): the model a service embeds with,
// so the settings can name the space its vectors are in.
func Default(c Connector, capability, input string) string {
	if c.Kind != Kind {
		return ""
	}
	s, err := Parse(c.Spec)
	if err != nil {
		return ""
	}
	b, _, err := s.binding(capability)
	if err != nil || b.Defaults[input] == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(b.Defaults[input]))
}

// Call runs capability on the approved service connector named name, records
// how it went and what it cost, and returns its result.
func Call(ctx context.Context, sess *ToolSession, name, capability string, inputs map[string]any) (Result, error) {
	c, ok := GetConnector(RootDB, name)
	if !ok || c.Kind != Kind {
		return nil, fmt.Errorf("no service connector named %q", name)
	}
	// An agent's tool reaching it, keyed or not: its scope and the users the
	// connection is limited to decide. A call with no session is a capability
	// the instance chose for everyone.
	if err := sess.ConnectionBarred(name); err != nil {
		return nil, err
	}
	if !callable(ctx, c) {
		return nil, fmt.Errorf("service %q is not in use: an admin puts it to use by choosing it on the page that uses it (Web Search, Image Generation, Research Sources, a flow, ...)", name)
	}
	s, err := specOf(c)
	if err == nil {
		s, err = settledFor(ctx, name, s)
	}
	if err != nil {
		return nil, err
	}
	out, err := s.run(ctx, sess, capability, inputs)
	noteCall(ctx, name, capability, out, err)
	return out.result, err
}

// generateImage is the image backend a service registers.
func generateImage(ctx context.Context, name, prompt string, landscape bool) (*ImageGenResult, error) {
	c, ok := GetConnector(RootDB, name)
	if !ok {
		return nil, fmt.Errorf("no service connector named %q", name)
	}
	s, err := specOf(c)
	if err == nil {
		s, err = settledFor(ctx, name, s)
	}
	if err != nil {
		return nil, err
	}
	b, _, err := s.binding("image.generate")
	if err != nil {
		return nil, err
	}
	w, h := number(b.Defaults["width"], 1024), number(b.Defaults["height"], 1024)
	inputs := map[string]any{"prompt": prompt}
	if landscape && len(b.Wide) > 0 {
		for k, v := range b.Wide {
			inputs[k] = v
		}
	} else if landscape && h > w {
		w, h = h, w
	}
	if _, set := inputs["width"]; !set {
		inputs["width"], inputs["height"] = w, h
	}
	res, err := Call(ctx, nil, name, "image.generate", inputs)
	if err != nil {
		return nil, err
	}
	return &ImageGenResult{URL: res["image"].File.Path, Prompt: prompt}, nil
}

// editImage is the edit backend a service registers: the source picture
// written to a file, sent as the contract's image input.
func editImage(sess *ToolSession, name, prompt string, image []byte) (*ImageGenResult, error) {
	f, _, err := writeFile(bytes.NewReader(image), defaultMaxBytes)
	if err != nil {
		return nil, err
	}
	defer os.Remove(f.Path)
	ctx := context.Background()
	if sess != nil && sess.Ctx != nil {
		ctx = sess.Ctx
	}
	res, err := Call(ctx, sess, name, "image.edit", map[string]any{"prompt": prompt, "image": f})
	if err != nil {
		return nil, err
	}
	return &ImageGenResult{URL: res["image"].File.Path, Prompt: prompt}, nil
}

// number reads a JSON number default, def when absent.
func number(v any, def int) int {
	switch n := v.(type) {
	case float64:
		if n > 0 {
			return int(n)
		}
	case int:
		if n > 0 {
			return n
		}
	}
	return def
}

// freeConnectorName is name, or name-2, name-3 ... when it is taken.
func freeConnectorName(db Database, name string) string {
	for i := 1; ; i++ {
		n := name
		if i > 1 {
			n = fmt.Sprintf("%s-%d", name, i)
		}
		if _, taken := GetConnector(db, n); !taken {
			return n
		}
	}
}

// freeCredentialName is name, or name_2, name_3 ... when it is taken.
func freeCredentialName(name string) string {
	for i := 1; ; i++ {
		n := name
		if i > 1 {
			n = fmt.Sprintf("%s_%d", name, i)
		}
		if _, taken := Secure().Resolve(n, ""); !taken {
			return n
		}
	}
}
