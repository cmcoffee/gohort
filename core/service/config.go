package service

// A connection's settings and secrets. The spec declares what the connection
// needs (config): each setting's kind of value, which decides the control a
// page draws for it and what a save accepts, and where it is used, as
// {config.<name>} anywhere in the spec. A setting may sit under one
// capability in provides (that capability's model), apart from the
// provider's (an address, a region). The values live in the connection's own
// space (core connection_space.go), never in the spec: the spec says how the
// service is reached and read, the space says with what.
//
// A secret is a setting of type secret. Its value is kept in the space's
// secret half, with the addresses it was given for, and is never a
// {config.x}: auth names it, and the call is made through it only at those
// addresses.
//
// A call takes the values on its context (WithConfig and WithSecrets: a
// page's Test trying what its form holds before a save), or else those the
// connection keeps, or else the defaults.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/keystore"
)

// ConfigField is one setting a connection takes.
type ConfigField struct {
	Name string `json:"name"`
	// Label is what a page calls it; Help one line under it; Detail the
	// rest, behind the page's info icon; Placeholder the greyed example in
	// an empty box.
	Label       string `json:"label,omitempty"`
	Help        string `json:"help,omitempty"`
	Detail      string `json:"detail,omitempty"`
	Placeholder string `json:"placeholder,omitempty"`
	// Type is what kind of value it is: text (the default), longtext, url,
	// number, toggle, choice (one of Options), choices (any of them) or
	// secret.
	Type     string `json:"type,omitempty"`
	Default  string `json:"default,omitempty"`
	Required bool   `json:"required,omitempty"`
	// Options are a choice's or choices' values, each with a label.
	Options []Option `json:"options,omitempty"`
	// OptionsFrom names an operation of the connection whose reply lists
	// values to pick from (its models, say), for text or a choice.
	OptionsFrom string `json:"options_from,omitempty"`
	// Pattern is a regular expression a text value must match.
	Pattern string `json:"pattern,omitempty"`
	// Rows is how tall a longtext box is.
	Rows int `json:"rows,omitempty"`
	// Min, Max and Decimals bound a number.
	Min      *float64 `json:"min,omitempty"`
	Max      *float64 `json:"max,omitempty"`
	Decimals int      `json:"decimals,omitempty"`
	// Send is how a secret goes out: bearer, header:<name>, query:<name>,
	// basic:<username> or aws_sigv4 (whose value is
	// ACCESS_KEY_ID:SECRET_ACCESS_KEY, or blank to sign as this machine under
	// Profile, for a connection an administrator made).
	Send    string `json:"send,omitempty"`
	Profile string `json:"profile,omitempty"`
	// Also are addresses a secret is sent to beside the provider's base URL
	// (a second host the service answers on).
	Also []string `json:"also,omitempty"`
}

// Option is one value of a choice, and what a page shows for it.
type Option struct {
	Value string `json:"value"`
	Label string `json:"label,omitempty"`
}

// UnmarshalJSON reads an option written as a bare value too.
func (o *Option) UnmarshalJSON(raw []byte) error {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		*o = Option{Value: s}
		return nil
	}
	type plain Option
	return json.Unmarshal(raw, (*plain)(o))
}

// Title is the field's label, or its name.
func (f ConfigField) Title() string {
	if l := strings.TrimSpace(f.Label); l != "" {
		return l
	}
	return f.Name
}

// kind is the field's type, with the older spelling read: select is choice.
func (f ConfigField) kind() string {
	switch t := strings.TrimSpace(f.Type); t {
	case "":
		return "text"
	case "select":
		return "choice"
	default:
		return t
	}
}

// IsSecret reports whether the setting is a secret.
func (f ConfigField) IsSecret() bool { return f.kind() == "secret" }

// Kind is the setting's type as a page draws it.
func (f ConfigField) Kind() string { return f.kind() }

// configRE is a setting's token: {config.<name>}.
var configRE = regexp.MustCompile(`\{config\.([a-z_][a-z0-9_]*)\}`)

var configNameRE = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

var settingTypes = map[string]bool{"text": true, "longtext": true, "url": true, "number": true, "toggle": true,
	"choice": true, "choices": true, "secret": true}

// Setting is one setting as a connection keeps it: Key is where its value is
// kept (its name, or <capability>/<name> for one under a capability).
type Setting struct {
	Key        string
	Capability string
	Field      ConfigField
}

// settingKey is where a setting's value is kept.
func settingKey(capability, name string) string {
	if capability == "" {
		return name
	}
	return capability + "/" + name
}

// settings are every setting the spec declares: the provider's, then each
// capability's.
func (s Spec) settings() []Setting {
	var out []Setting
	for _, f := range s.Config {
		out = append(out, Setting{Key: f.Name, Field: f})
	}
	for _, b := range s.Provides {
		for _, f := range b.Config {
			out = append(out, Setting{Key: settingKey(b.Capability, f.Name), Capability: b.Capability, Field: f})
		}
	}
	return out
}

// checkField checks one declared setting.
func checkField(where string, f ConfigField, provider bool) error {
	if !configNameRE.MatchString(f.Name) {
		return fmt.Errorf("%s: name %q: lowercase letters, digits and _", where, f.Name)
	}
	k := f.kind()
	if !settingTypes[k] {
		return fmt.Errorf("%s %s: type %q is text, longtext, url, number, toggle, choice, choices or secret", where, f.Name, f.Type)
	}
	if (k == "choice" || k == "choices") && len(f.Options) == 0 && f.OptionsFrom == "" {
		return fmt.Errorf("%s %s: a %s needs options, or options_from", where, f.Name, k)
	}
	if f.Pattern != "" {
		if _, err := regexp.Compile(f.Pattern); err != nil {
			return fmt.Errorf("%s %s: pattern: %v", where, f.Name, err)
		}
	}
	if k == "number" && f.Min != nil && f.Max != nil && *f.Min > *f.Max {
		return fmt.Errorf("%s %s: min is above max", where, f.Name)
	}
	if k == "secret" {
		if !provider {
			return fmt.Errorf("%s %s: a secret belongs to the provider, which sends it: declare it in the spec's config", where, f.Name)
		}
		how, _, _ := strings.Cut(f.Send, ":")
		switch how {
		case "bearer", "aws_sigv4", "basic":
		case "header", "query":
			if _, p, _ := strings.Cut(f.Send, ":"); strings.TrimSpace(p) == "" {
				return fmt.Errorf("%s %s: send %q names no %s", where, f.Name, f.Send, how)
			}
		default:
			return fmt.Errorf("%s %s: send is bearer, header:<name>, query:<name>, basic:<username> or aws_sigv4", where, f.Name)
		}
		if f.Default != "" {
			return fmt.Errorf("%s %s: a secret has no default", where, f.Name)
		}
	} else if f.Send != "" || f.Profile != "" || len(f.Also) > 0 {
		return fmt.Errorf("%s %s: send, profile and also are a secret's", where, f.Name)
	}
	if f.Default != "" && k != "secret" {
		if err := checkValue(f, f.Default); err != nil {
			return fmt.Errorf("%s %s: default: %v", where, f.Name, err)
		}
	}
	return nil
}

// validateConfig checks the declared settings, that every {config.x} the
// spec uses is one it may (a capability's own, or the provider's), that none
// is a secret, and that auth names a secret.
func (s Spec) validateConfig() error {
	provider := map[string]ConfigField{}
	for i, f := range s.Config {
		if err := checkField(fmt.Sprintf("config[%d]", i), f, true); err != nil {
			return err
		}
		if _, dup := provider[f.Name]; dup {
			return fmt.Errorf("config: %s is declared twice", f.Name)
		}
		provider[f.Name] = f
	}
	check := func(where string, node any, scope map[string]ConfigField) error {
		raw, _ := json.Marshal(node)
		for _, m := range configRE.FindAllStringSubmatch(string(raw), -1) {
			f, ok := scope[m[1]]
			if !ok {
				return fmt.Errorf("%s uses {config.%s}, which config does not declare", where, m[1])
			}
			if f.IsSecret() {
				return fmt.Errorf("%s uses {config.%s}, a secret: a secret is sent by auth, never written into a request", where, m[1])
			}
		}
		return nil
	}
	if err := check("the provider", s.Provider, provider); err != nil {
		return err
	}
	for i, f := range s.Config {
		if err := check("config "+f.Name, s.Config[i], provider); err != nil {
			return err
		}
	}
	for _, b := range s.Provides {
		scope := map[string]ConfigField{}
		for k, v := range provider {
			scope[k] = v
		}
		seen := map[string]bool{}
		for _, f := range b.Config {
			if err := checkField(b.Capability+" config", f, false); err != nil {
				return err
			}
			if _, clash := provider[f.Name]; clash || seen[f.Name] {
				return fmt.Errorf("%s config: %s is declared twice", b.Capability, f.Name)
			}
			seen[f.Name] = true
			scope[f.Name] = f
		}
		b.Config = nil
		if err := check(b.Capability, b, scope); err != nil {
			return err
		}
	}
	if name := strings.TrimSpace(s.Provider.Auth.Secret); name != "" {
		f, ok := provider[name]
		if !ok || !f.IsSecret() {
			return fmt.Errorf("auth names the secret %q, which config does not declare as a secret", name)
		}
	}
	return nil
}

// checkValue checks a value given for setting f.
func checkValue(f ConfigField, v string) error {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	switch f.kind() {
	case "url":
		u, err := url.Parse(v)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return fmt.Errorf("%q is not an http or https address", v)
		}
	case "number":
		n, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return fmt.Errorf("%q is not a number", v)
		}
		if f.Min != nil && n < *f.Min {
			return fmt.Errorf("%s is below %s", v, strconv.FormatFloat(*f.Min, 'f', -1, 64))
		}
		if f.Max != nil && n > *f.Max {
			return fmt.Errorf("%s is above %s", v, strconv.FormatFloat(*f.Max, 'f', -1, 64))
		}
	case "toggle":
		if _, err := strconv.ParseBool(v); err != nil {
			return fmt.Errorf("%q is not on or off", v)
		}
	case "choice", "choices":
		if len(f.Options) == 0 {
			return nil
		}
		for _, one := range splitChoices(f, v) {
			ok := false
			for _, o := range f.Options {
				ok = ok || o.Value == one
			}
			if !ok {
				return fmt.Errorf("%q is not one of its options", one)
			}
		}
	case "text":
		if f.Pattern != "" {
			if re, err := regexp.Compile(f.Pattern); err == nil && !re.MatchString(v) {
				return fmt.Errorf("%q is not in the form it takes", v)
			}
		}
	}
	return nil
}

// splitChoices is a value's choices: a choices value is them joined by
// commas, a choice's is one.
func splitChoices(f ConfigField, v string) []string {
	if f.kind() != "choices" {
		return []string{strings.TrimSpace(v)}
	}
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// CheckSettings checks values a page or a row is saving for connection c,
// by key: each a value its setting takes, none it does not declare, and
// every required one there (a capability's only when that capability is
// asked for, or always with capability ""). A secret is checked apart: its
// value is not among values.
func CheckSettings(c Connector, capability string, values map[string]string) error {
	s, err := Parse(c.Spec)
	if err != nil {
		return err
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
	for _, k := range keys {
		st, ok := declared[k]
		if !ok || st.Field.IsSecret() {
			return fmt.Errorf("%s takes no setting %q", c.Name, k)
		}
		if err := checkValue(st.Field, values[k]); err != nil {
			return fmt.Errorf("%s: %v", st.Field.Title(), err)
		}
	}
	var missing []string
	for _, st := range s.settings() {
		if !st.Field.Required || st.Field.IsSecret() || st.Field.Default != "" {
			continue
		}
		if st.Capability != "" && capability != "" && st.Capability != capability {
			continue
		}
		if strings.TrimSpace(values[st.Key]) == "" {
			missing = append(missing, st.Field.Title())
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%s needs %s", c.Name, strings.Join(missing, ", "))
	}
	return nil
}

// UsesConfig reports whether the spec declares settings.
func (s Spec) UsesConfig() bool { return len(s.settings()) > 0 }

// placeholders are values that let a spec be checked without a connection's
// own: each default, or a stand-in of its type.
func (s Spec) placeholders() map[string]string {
	out := map[string]string{}
	for _, st := range s.settings() {
		f := st.Field
		v := f.Default
		if v == "" {
			switch f.kind() {
			case "url":
				v = "https://example.invalid"
			case "number":
				v = "1"
				if f.Min != nil {
					v = strconv.FormatFloat(*f.Min, 'f', -1, 64)
				}
			case "toggle":
				v = "false"
			case "choice", "choices":
				if len(f.Options) > 0 {
					v = f.Options[0].Value
				} else {
					v = "x"
				}
			case "secret":
				continue
			default:
				v = "x"
			}
		}
		out[st.Key] = v
	}
	return out
}

// settle is the spec with values (by key) in place of its {config.x}: each
// from values, else its default. The provider's settings fill the provider
// and every capability; a capability's own fill it. A required provider
// setting with neither is refused; a capability's is refused when that
// capability is asked for (binding). A token that is a whole JSON string of
// a number or toggle setting becomes the number or the boolean.
func (s Spec) settle(values map[string]string) (Spec, error) {
	if !s.UsesConfig() {
		return s, nil
	}
	fill := func(f ConfigField, key string) string {
		v := strings.TrimSpace(values[key])
		if v == "" {
			v = f.Default
		}
		return v
	}
	vals, types := map[string]string{}, map[string]string{}
	var missing []string
	for _, f := range s.Config {
		if f.IsSecret() {
			continue
		}
		v := fill(f, f.Name)
		if v == "" && f.Required {
			missing = append(missing, f.Title())
		}
		vals[f.Name], types[f.Name] = v, f.kind()
	}
	if len(missing) > 0 {
		return s, fmt.Errorf("set %s for this connection: on the page that uses it, or on its row under Admin > Services > Connectors", strings.Join(missing, ", "))
	}
	out := s
	var err error
	if out.Provider, err = settleAs(s.Provider, vals, types); err != nil {
		return s, err
	}
	// A secret's profile and also may be settings too.
	out.Config = make([]ConfigField, len(s.Config))
	for i, f := range s.Config {
		if out.Config[i], err = settleAs(f, vals, types); err != nil {
			return s, err
		}
	}
	out.Provides = make([]Binding, len(s.Provides))
	out.missing = map[string][]string{}
	for i, b := range s.Provides {
		bv, bt := map[string]string{}, map[string]string{}
		for k, v := range vals {
			bv[k], bt[k] = v, types[k]
		}
		for _, f := range b.Config {
			v := fill(f, settingKey(b.Capability, f.Name))
			if v == "" && f.Required {
				out.missing[b.Capability] = append(out.missing[b.Capability], f.Title())
			}
			bv[f.Name], bt[f.Name] = v, f.kind()
		}
		cfg := b.Config
		b.Config = nil
		if out.Provides[i], err = settleAs(b, bv, bt); err != nil {
			return s, err
		}
		out.Provides[i].Config = cfg
	}
	return out, nil
}

// settleAs is v, a part of a spec, with its tokens filled.
func settleAs[T any](v T, vals, types map[string]string) (T, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return v, err
	}
	var node any
	if err := json.Unmarshal(raw, &node); err != nil {
		return v, err
	}
	raw, err = json.Marshal(settleValue(node, vals, types))
	if err != nil {
		return v, err
	}
	var out T
	if err := json.Unmarshal(raw, &out); err != nil {
		return v, err
	}
	return out, nil
}

func settleValue(v any, vals, types map[string]string) any {
	switch t := v.(type) {
	case string:
		if m := configRE.FindStringSubmatch(t); m != nil && m[0] == t {
			switch types[m[1]] {
			case "number":
				if n, err := strconv.ParseFloat(vals[m[1]], 64); err == nil {
					return n
				}
			case "toggle":
				if b, err := strconv.ParseBool(vals[m[1]]); err == nil {
					return b
				}
			}
		}
		return configRE.ReplaceAllStringFunc(t, func(tok string) string {
			return vals[configRE.FindStringSubmatch(tok)[1]]
		})
	case map[string]any:
		for k, x := range t {
			t[k] = settleValue(x, vals, types)
		}
	case []any:
		for i, x := range t {
			t[i] = settleValue(x, vals, types)
		}
	}
	return v
}

type configKey struct{}
type secretsKey struct{}

// WithConfig carries a form's values for the connection it is testing (a
// Test before its save), in place of what the connection keeps.
func WithConfig(ctx context.Context, values map[string]string) context.Context {
	return context.WithValue(ctx, configKey{}, values)
}

// WithSecrets carries secrets typed on a form for the connection it is
// testing, by name, over those it keeps: they go to the address the form
// gives.
func WithSecrets(ctx context.Context, values map[string]string) context.Context {
	return context.WithValue(ctx, secretsKey{}, values)
}

// connectionAuth is how a settled spec's calls carry the connection's
// secret: resolved when it is settled, refused (err) only when a call needs
// it.
type connectionAuth struct {
	cred SecureCredential
	err  error
}

// settledFor is connection name's spec s with its settings filled for a
// call under ctx (the values on ctx, else those it keeps, else the
// defaults), and its secret ready for the calls it makes.
func settledFor(ctx context.Context, name string, s Spec) (Spec, error) {
	if cred := strings.TrimSpace(s.Provider.Auth.Credential); cred != "" {
		return s, fmt.Errorf("%s sends its key with the vault credential %s, which connections no longer do: a connection keeps its own key. Add it again from its template (or declare a secret setting in its spec and name it in auth.secret), and give the key where it is used", name, cred)
	}
	c, _ := GetConnector(RootDB, name)
	values, ok := ctx.Value(configKey{}).(map[string]string)
	if !ok {
		values = ConnectionConfig(RootDB, c)
	}
	out, err := s.settle(values)
	if err != nil {
		return s, err
	}
	typed, _ := ctx.Value(secretsKey{}).(map[string]string)
	out.auth = out.resolveSecret(c, typed)
	return out, nil
}

// secretField is the secret auth names, if any.
func (s Spec) secretField() (ConfigField, bool) {
	name := strings.TrimSpace(s.Provider.Auth.Secret)
	if name == "" {
		return ConfigField{}, false
	}
	for _, f := range s.Config {
		if f.Name == name && f.IsSecret() {
			return f, true
		}
	}
	return ConfigField{}, false
}

// secretAddresses are the addresses a settled spec's secret goes to: its
// base URL, and the secret's also.
func (s Spec) secretAddresses(f ConfigField) []string {
	out := []string{strings.TrimRight(strings.TrimSpace(s.Provider.BaseURL), "/")}
	for _, a := range f.Also {
		if a = strings.TrimRight(strings.TrimSpace(a), "/"); a != "" {
			out = append(out, a)
		}
	}
	return out
}

// resolveSecret is how settled spec s's calls carry connection c's secret:
// one typed for a Test, at the address the spec now gives; else the one it
// keeps, only while the spec gives the addresses it was given for.
func (s Spec) resolveSecret(c Connector, typed map[string]string) *connectionAuth {
	f, ok := s.secretField()
	if !ok {
		return nil
	}
	addrs := s.secretAddresses(f)
	profile, fromKeystore := f.Profile, false
	value, have := typed[f.Name]
	if !have || strings.TrimSpace(value) == "" {
		kept, ok := GetConnectionSecret(RootDB, c, f.Name)
		if ok && kept.Keystore != "" {
			v, p, err := keystoreSecret(kept.Keystore, f, addrs)
			if err != nil {
				return &connectionAuth{err: fmt.Errorf("%s: %w", c.Name, err)}
			}
			value, fromKeystore = v, true
			if p != "" {
				profile = p
			}
		} else if ok {
			if !sameAddresses(kept.Addresses, addrs) {
				return &connectionAuth{err: fmt.Errorf("%s's %s was given for %s, and the connection now goes to %s: give the %s again for the new address",
					c.Name, f.Title(), strings.Join(kept.Addresses, ", "), strings.Join(addrs, ", "), f.Title())}
			}
			value = kept.Value
		} else {
			value = ""
		}
	}
	// A key it may go without (a local server started with no --api-key),
	// not given, sends none.
	if strings.TrimSpace(value) == "" && !f.Required && !strings.HasPrefix(f.Send, "aws_sigv4") {
		return nil
	}
	// Signing as this machine is the deployment's identity: a connection
	// drafted by someone who is not an administrator signs with a key pair.
	// A Keystore entry's profile is an administrator's choice.
	if strings.HasPrefix(f.Send, "aws_sigv4") && strings.TrimSpace(value) == "" && !fromKeystore && c.Owner != "" && !UserIsAdmin(c.Owner) {
		return &connectionAuth{err: fmt.Errorf("%s was drafted by %s, who is not an administrator: it does not sign as this machine. Give it an AWS key pair", c.Name, c.Owner)}
	}
	cred, err := ConnectionCredential(c.Name, f.Send, value, profile, addrs)
	return &connectionAuth{cred: cred, err: err}
}

// sameAddresses reports whether a secret given for kept may go to now: the
// same addresses, in any order.
func sameAddresses(kept, now []string) bool {
	if len(kept) != len(now) {
		return false
	}
	set := map[string]bool{}
	for _, a := range kept {
		set[strings.ToLower(strings.TrimRight(a, "/"))] = true
	}
	for _, a := range now {
		if !set[strings.ToLower(strings.TrimRight(a, "/"))] {
			return false
		}
	}
	return true
}

// SettingsFor are the settings connection c takes for capability: the
// provider's, and that capability's own (every capability's with "").
func SettingsFor(c Connector, capability string) []Setting {
	if c.Kind != Kind {
		return nil
	}
	s, err := Parse(c.Spec)
	if err != nil {
		return nil
	}
	var out []Setting
	for _, st := range s.settings() {
		if st.Capability == "" || capability == "" || st.Capability == capability {
			out = append(out, st)
		}
	}
	return out
}

// SaveSettings keeps values (by key) as connection c's settings, checked as
// CheckSettings checks them, and any secret given in secrets (by name, blank
// keeping the one it has), for the addresses the settings now give. A secret
// kept for other addresses is refused unless it is given again.
func SaveSettings(c Connector, capability string, values, secrets map[string]string) error {
	if err := CheckSettings(c, capability, values); err != nil {
		return err
	}
	s, err := Parse(c.Spec)
	if err != nil {
		return err
	}
	settled, err := s.settle(values)
	if err != nil {
		return err
	}
	for name, v := range secrets {
		if strings.TrimSpace(v) == "" {
			continue
		}
		f, ok := settled.secretNamed(name)
		if !ok {
			return fmt.Errorf("%s keeps no secret %q", c.Name, name)
		}
		if id, ref := isKeystoreValue(v); ref {
			if id == "" {
				if f.Required {
					return fmt.Errorf("give the %s, or pick a key from the Keystore", f.Title())
				}
				continue
			}
			e, ok := keystore.Get(RootDB, id)
			if !ok {
				return fmt.Errorf("no Keystore key %s", id)
			}
			if err := keystoreFits(e, f, settled.secretAddresses(f)); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(f.Send, "aws_sigv4") {
			if _, err := ConnectionCredential(c.Name, f.Send, v, "", []string{"https://x"}); err != nil {
				return fmt.Errorf("%s: %v", f.Title(), err)
			}
		}
	}
	if f, ok := settled.secretField(); ok {
		if strings.TrimSpace(secrets[f.Name]) == "" {
			if kept, ok := GetConnectionSecret(RootDB, c, f.Name); ok && kept.Keystore != "" {
				if _, _, err := keystoreSecret(kept.Keystore, f, settled.secretAddresses(f)); err != nil {
					return err
				}
			} else if ok && !sameAddresses(kept.Addresses, settled.secretAddresses(f)) {
				return fmt.Errorf("the %s was given for %s: give it again for %s", f.Title(), strings.Join(kept.Addresses, ", "), strings.Join(settled.secretAddresses(f), ", "))
			}
		}
	}
	if err := SetConnectionConfig(RootDB, c, values); err != nil {
		return err
	}
	for name, v := range secrets {
		if strings.TrimSpace(v) == "" {
			continue
		}
		if id, ref := isKeystoreValue(v); ref {
			if id == "" {
				UnsetConnectionSecret(RootDB, c, name)
			} else if err := SetConnectionSecretRef(RootDB, c, name, id); err != nil {
				return err
			}
			continue
		}
		f, _ := settled.secretNamed(name)
		if err := SetConnectionSecret(RootDB, c, name, v, settled.secretAddresses(f)); err != nil {
			return err
		}
	}
	return nil
}

// secretNamed is the secret setting called name.
func (s Spec) secretNamed(name string) (ConfigField, bool) {
	for _, f := range s.Config {
		if f.Name == name && f.IsSecret() {
			return f, true
		}
	}
	return ConfigField{}, false
}

// HasSecret reports whether connection c keeps its secret name.
func HasSecret(c Connector, name string) bool {
	s, ok := GetConnectionSecret(RootDB, c, name)
	return ok && (s.Value != "" || s.Keystore != "")
}

// hintLength is how long a key must be before its last four characters are
// shown: four of a short one give too much of it away.
const hintLength = 16

// KeptSecrets are what a page says of each secret connection c keeps, by
// its setting's key, so one key can be told from another without showing
// it: the last four characters of a key long enough that four give little
// away, none of a password, and the day it was given. One kept but
// unreadable says so.
func KeptSecrets(db Database, c Connector) map[string]string {
	if c.Kind != Kind {
		return nil
	}
	s, err := specOf(c)
	if err != nil {
		return nil
	}
	unreadable := c.UnreadableSecrets(db)
	out := map[string]string{}
	for _, f := range s.Config {
		if !f.IsSecret() {
			continue
		}
		if slices.Contains(unreadable, f.Name) {
			out[f.Name] = "kept but cannot be read: give it again"
			continue
		}
		kept, ok := GetConnectionSecret(db, c, f.Name)
		if ok && kept.Keystore != "" {
			if e, found := keystore.Get(db, kept.Keystore); found {
				out[f.Name] = "from the Keystore: " + e.Name + " (" + keystore.Hint(db, e) + ")"
			} else {
				out[f.Name] = "names a Keystore key that is gone: pick another"
			}
			continue
		}
		if !ok || kept.Value == "" {
			continue
		}
		hint, v := "set", []rune(kept.Value)
		if !strings.HasPrefix(f.Send, "basic") && len(v) >= hintLength {
			hint = "ends in …" + string(v[len(v)-4:])
		}
		if !kept.Set.IsZero() {
			hint += ", given " + kept.Set.Format("2 Jan 2006")
		}
		out[f.Name] = hint
	}
	return out
}

// DefaultWith is what connection c runs capability's input with when the
// caller gives none, under values (by key, nil for what it keeps): the
// binding's default with its {config.x} filled, or else the setting of the
// same name. It is how a page names the model its choice embeds with.
func DefaultWith(c Connector, capability, input string, values map[string]string) string {
	if c.Kind != Kind {
		return ""
	}
	s, err := Parse(c.Spec)
	if err != nil {
		return ""
	}
	if values == nil {
		values = ConnectionConfig(RootDB, c)
	}
	// A required setting not set takes a stand-in, so what does not depend
	// on it still reads.
	merged := s.placeholders()
	for k, v := range values {
		if strings.TrimSpace(v) != "" {
			merged[k] = v
		}
	}
	for _, st := range s.settings() {
		if strings.TrimSpace(values[st.Key]) == "" && st.Field.Default != "" {
			merged[st.Key] = st.Field.Default
		}
	}
	settled, err := s.settle(merged)
	if err != nil {
		return ""
	}
	b, _, err := settled.binding(capability)
	if err != nil {
		return ""
	}
	if v, ok := b.Defaults[input]; ok && v != nil {
		return strings.TrimSpace(fmt.Sprint(v))
	}
	for _, key := range []string{settingKey(capability, input), input} {
		if v, ok := merged[key]; ok {
			if _, declared := s.declared(key); declared {
				return v
			}
		}
	}
	return ""
}

// declared is the setting kept under key.
func (s Spec) declared(key string) (Setting, bool) {
	for _, st := range s.settings() {
		if st.Key == key {
			return st, true
		}
	}
	return Setting{}, false
}

// ListOptions is what the operation a setting names in options_from lists,
// for a page to pick from: connection name settled under ctx (a form's
// values over what it keeps), asked through its own secret.
func ListOptions(ctx context.Context, name, key string) ([]string, error) {
	c, ok := GetConnector(RootDB, name)
	if !ok || c.Kind != Kind {
		return nil, fmt.Errorf("no service connector named %q", name)
	}
	if !callable(ctx, c) {
		return nil, fmt.Errorf("%s has a change waiting: choose it where it is used, or test it there first", name)
	}
	s, err := specOf(c)
	if err != nil {
		return nil, err
	}
	st, ok := s.declared(key)
	if !ok || st.Field.OptionsFrom == "" {
		return nil, fmt.Errorf("%s's setting %q lists no options", name, key)
	}
	settled, err := settledFor(ctx, name, s)
	if err != nil {
		return nil, err
	}
	r, err := settled.transport(nil).do(ctx, st.Field.OptionsFrom, map[string]any{})
	if err != nil {
		return nil, err
	}
	return optionValues(r.node), nil
}

// optionValues reads a list from a reply: the reply itself, or the list
// under data, models, voices, items or results; each item a value, or an
// object's id, name or value.
func optionValues(node any) []string {
	list, _ := node.([]any)
	if m, ok := node.(map[string]any); ok {
		for _, k := range []string{"data", "models", "voices", "items", "results"} {
			if l, ok := m[k].([]any); ok {
				list = l
				break
			}
		}
	}
	var out []string
	for _, it := range list {
		switch v := it.(type) {
		case string:
			out = append(out, v)
		case map[string]any:
			for _, k := range []string{"id", "name", "value", "voice_id"} {
				if s, ok := v[k].(string); ok && s != "" {
					out = append(out, s)
					break
				}
			}
		}
	}
	return out
}
