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
	if _, _, err := s.binding("image.generate"); err == nil {
		RegisterImageBackend(name, func(ctx context.Context, prompt string, landscape bool) (*ImageGenResult, error) {
			return generateImage(ctx, name, prompt, landscape)
		})
		ownedMu.Lock()
		owned[name] = true
		ownedMu.Unlock()
	} else {
		unregisterOwned(name)
	}
	if _, _, err := s.binding("image.edit"); err == nil {
		RegisterImageEditBackend(name, func(sess *ToolSession, prompt string, image []byte) (*ImageGenResult, error) {
			return editImage(sess, name, prompt, image)
		})
	} else {
		RegisterImageEditBackend(name, nil)
	}
	if _, _, err := s.binding("music.generate"); err == nil {
		registerMusicTool(name)
	}
	if b, _, err := s.binding("web.search"); err == nil {
		registerSearchTool(name, b)
	}
	if s.Provider.Health != nil {
		startHealth()
	}
	return nil
}

// Credentials are what the service spends, so a session denied its key is
// not offered it.
func (handler) Credentials(c Connector) []string {
	s, err := Parse(c.Spec)
	if err != nil || s.Provider.Auth.Credential == "" {
		return nil
	}
	return []string{s.Provider.Auth.Credential}
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
	if s.Provider.Auth.Credential != "" {
		auth = "key " + s.Provider.Auth.Credential
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
	if !c.Approved {
		return nil, fmt.Errorf("service %q is not approved yet: an admin approves it in Admin > Connectors", name)
	}
	s, err := specOf(c)
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
