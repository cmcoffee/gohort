package docs

// Publishing targets a person builds themselves: an API integration (or an
// agent) plus an instruction plus a short intake form. The destination that
// serves them lives in apps/publish; this file is the shape other packages
// need to see: the form a target asks, which agents may use it, and the seam
// that runs a publish through a credential.

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
)

// PublishField is one question a target asks each time: a category, a
// visibility, a space. Its answer reaches the publish as Answers[Name].
type PublishField struct {
	Name     string   `json:"name"`
	Label    string   `json:"label,omitempty"`
	Type     string   `json:"type,omitempty"` // "text" (default), "textarea" or "select"
	Options  []string `json:"options,omitempty"`
	Required bool     `json:"required,omitempty"`
	Help     string   `json:"help,omitempty"`
}

// PublishTargetSpec is a target with its form and the agents allowed to
// publish there through the publish tool.
type PublishTargetSpec struct {
	Kind   string         `json:"kind"`
	Target PublishTarget  `json:"target"`
	Fields []PublishField `json:"fields,omitempty"`
	Agents []string       `json:"agents,omitempty"` // agent ids
}

// TargetSpecSource is a destination that can describe its targets in full.
type TargetSpecSource interface {
	TargetSpecs(ctx context.Context, user string) []PublishTargetSpec
}

// PublishTargetSpecs gathers every target, with its form, that the user's
// destinations describe. Sorted by title.
func PublishTargetSpecs(ctx context.Context, user string) []PublishTargetSpec {
	publishMu.RLock()
	var srcs []TargetSpecSource
	for _, d := range publishDests {
		if s, ok := d.(TargetSpecSource); ok {
			if ok, _ := d.Available(user); ok {
				srcs = append(srcs, s)
			}
		}
	}
	publishMu.RUnlock()
	var out []PublishTargetSpec
	for _, s := range srcs {
		out = append(out, s.TargetSpecs(ctx, user)...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Target.Title < out[j].Target.Title })
	return out
}

// MissingAnswers names the required fields a set of answers leaves empty.
func MissingAnswers(fields []PublishField, answers map[string]string) []string {
	var out []string
	for _, f := range fields {
		if f.Required && strings.TrimSpace(answers[f.Name]) == "" {
			out = append(out, firstNonEmptyStr(f.Label, f.Name))
		}
	}
	return out
}

func firstNonEmptyStr(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

// CredentialPublisherFunc runs one publish through one credential: a short
// agent run holding only that credential's API tool, following instruction,
// that reports where the document landed. Registered by the package that owns
// the agent loop (orchestrate), like AgentPublisherFunc.
type CredentialPublisherFunc func(ctx context.Context, user, credential, instruction string) (said, url string, err error)

var (
	credPublisherMu sync.RWMutex
	credPublisher   CredentialPublisherFunc
)

// RegisterCredentialPublisher installs the credential-run closure.
func RegisterCredentialPublisher(fn CredentialPublisherFunc) {
	credPublisherMu.Lock()
	credPublisher = fn
	credPublisherMu.Unlock()
}

// CredentialPublisherReady reports whether a credential runner is installed.
func CredentialPublisherReady() bool {
	credPublisherMu.RLock()
	defer credPublisherMu.RUnlock()
	return credPublisher != nil
}

// PublishViaCredential runs a publish through the named credential.
func PublishViaCredential(ctx context.Context, user, credential, instruction string) (string, string, error) {
	credPublisherMu.RLock()
	fn := credPublisher
	credPublisherMu.RUnlock()
	if fn == nil {
		return "", "", errors.New("this deployment cannot publish through an API integration")
	}
	if strings.TrimSpace(credential) == "" {
		return "", "", errors.New("no API integration is named for this target")
	}
	return fn(ctx, user, credential, instruction)
}
