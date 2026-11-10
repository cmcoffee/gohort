package service

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"math"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"

	. "github.com/cmcoffee/gohort/core"
)

func init() { RegisterConnectorKind(Kind, handler{}) }

// handler is the service connector kind. A service adds new outside reach,
// so a page that names it is what reaches it (tieins.go).
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
	// An image builtin (ComfyUI, A1111) is served by the machinery that
	// runs one: generate, edit, blend and its tool alike.
	_, backend := imageOperation(s)
	if backend {
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
	if _, _, err := s.binding("image.edit"); err == nil && !backend {
		RegisterImageEditBackend(name, func() int { return editLimit(name) }, func(sess *ToolSession, prompt string, images [][]byte) (*ImageGenResult, error) {
			return editImage(sess, name, prompt, images)
		})
	} else {
		RegisterImageEditBackend(name, nil, nil)
	}
	if _, _, err := s.binding("music.generate"); err == nil {
		registerMusicTool(name)
	}
	if _, _, err := s.binding("media.generate"); err == nil {
		registerMediaTool(name)
	}
	if s.Provider.Health != nil {
		startHealth()
	}
	return nil
}

// ServedAlways: a connection carries no approval. It is served from its
// save, and reached through the pages that name it (tieins.go).
func (handler) ServedAlways() bool { return true }

func (handler) Teardown(c Connector) error {
	name := strings.TrimSpace(c.Name)
	ownedMu.Lock()
	mine := owned[name]
	delete(owned, name)
	ownedMu.Unlock()
	if mine {
		UnregisterImageBackend(name)
	}
	RegisterImageEditBackend(name, nil, nil)
	if s, err := Parse(c.Spec); err == nil {
		if _, backend := imageOperation(s); backend {
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

// Call runs capability on the service connector named name, records how it
// went and what it cost, and returns its result. Whatever calls it has the
// name from a reference to it (a page's choice, a Research Source, ...).
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
		return nil, fmt.Errorf("service %q has a change waiting: an admin accepts it by saving a page that uses it, after reading its spec under Connectors", name)
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

// editAspects are the shapes an image model is asked for by name (Gemini's
// list), as width over height.
var editAspects = []struct {
	name  string
	ratio float64
}{
	{"1:1", 1}, {"2:3", 2.0 / 3}, {"3:2", 3.0 / 2}, {"3:4", 3.0 / 4}, {"4:3", 4.0 / 3},
	{"4:5", 4.0 / 5}, {"5:4", 5.0 / 4}, {"9:16", 9.0 / 16}, {"16:9", 16.0 / 9}, {"21:9", 21.0 / 9},
}

// editAspect is the named shape nearest a picture's own, "" when its size
// cannot be read (a WebP, which the standard library does not decode).
func editAspect(data []byte) string {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return ""
	}
	r := math.Log(float64(cfg.Width) / float64(cfg.Height))
	best, gap := "", math.Inf(1)
	for _, a := range editAspects {
		if d := math.Abs(r - math.Log(a.ratio)); d < gap {
			best, gap = a.name, d
		}
	}
	return best
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

// editPictureInputs are image.edit's source-picture inputs, in order: the
// first is required, the rest make it a blend or a composite.
var editPictureInputs = []string{"image", "image2", "image3", "image4", "image5", "image6", "image7", "image8", "image9", "image10"}

// editPictures is how many source pictures b's operations take: image, then
// as many of image2 to image10, in order, as they use.
func (s Spec) editPictures(b Binding) int {
	used := map[string]bool{}
	for _, st := range b.Steps {
		for _, tok := range opTokens(s.Provider.Operations[st.Call+st.Wait]) {
			used[tok] = true
		}
	}
	n := 1
	for _, in := range editPictureInputs[1:] {
		if !used[in] {
			break
		}
		n++
	}
	return n
}

// editLimit is how many source pictures connector name's image.edit takes
// now, read from its spec and settings on each use: the picture inputs its
// operations use, held to its pictures (a number, or the setting it names)
// when that is fewer. A connector that does not read takes one.
func editLimit(name string) int {
	c, ok := GetConnector(RootDB, name)
	if !ok {
		return 1
	}
	s, err := specOf(c)
	if err != nil {
		return 1
	}
	if settled, err := s.settle(ConnectionConfig(RootDB, c)); err == nil {
		s = settled
	}
	b, _, err := s.binding("image.edit")
	if err != nil {
		return 1
	}
	n := s.editPictures(b)
	if v, ok := b.Pictures.(float64); ok && int(v) >= 1 && int(v) < n {
		n = int(v)
	}
	return n
}

// editImage is the edit backend a service registers: each source picture
// written to a file and sent as the contract's image inputs, in order.
func editImage(sess *ToolSession, name, prompt string, images [][]byte) (*ImageGenResult, error) {
	if len(images) == 0 || len(images) > len(editPictureInputs) {
		return nil, fmt.Errorf("image.edit takes 1 to %d pictures, not %d", len(editPictureInputs), len(images))
	}
	vars := map[string]any{"prompt": prompt}
	// The shape of the picture being edited, for a template that sets the
	// output's: left to itself, a model given a scene and a portrait came
	// back in the portrait's shape, the scene cut to fit.
	if a := editAspect(images[0]); a != "" {
		vars["aspect"] = a
	}
	for i, data := range images {
		f, _, err := writeFile(bytes.NewReader(data), defaultMaxBytes)
		if err != nil {
			return nil, err
		}
		defer os.Remove(f.Path)
		vars[editPictureInputs[i]] = f
	}
	ctx := context.Background()
	if sess != nil && sess.Ctx != nil {
		ctx = sess.Ctx
	}
	res, err := Call(ctx, sess, name, "image.edit", vars)
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
