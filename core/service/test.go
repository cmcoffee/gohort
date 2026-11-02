package service

import (
	"context"
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// TestReport is how one capability's contract test went.
type TestReport struct {
	Capability string  `json:"capability"`
	OK         bool    `json:"ok"`
	Detail     string  `json:"detail"`
	Cost       float64 `json:"cost,omitempty"`
	Priced     bool    `json:"priced"`
	// File is what came back, for a person to look at.
	File string `json:"file,omitempty"`
}

// Estimate is what a contract test of connector name is expected to cost,
// and whether that is known, for the question asked before one runs.
func Estimate(name string) (float64, bool) {
	c, ok := GetConnector(RootDB, name)
	if !ok || c.Kind != Kind {
		return 0, false
	}
	s, err := Parse(c.Spec)
	if err != nil {
		return 0, false
	}
	total, known := 0.0, true
	for _, b := range s.Provides {
		v, k := b.estimate()
		total += v
		known = known && k
	}
	return total, known
}

// Testable reports whether connector c has a contract test: a service.
func Testable(c Connector) bool { return c.Kind == Kind }

// Test runs each capability's contract test: the smallest real call that
// proves the capability, through the contract's check. A real call can cost
// money, so tests draw on a daily budget per connector, and a test past it
// is refused with what it would cost.
func Test(ctx context.Context, name string) ([]TestReport, error) {
	c, ok := GetConnector(RootDB, name)
	if !ok {
		return nil, fmt.Errorf("no connector named %q", name)
	}
	switch c.Kind {
	case Kind:
		s, err := specOf(c)
		if err != nil {
			return nil, err
		}
		if err := takeTest(name, s); err != nil {
			return nil, err
		}
		return runTests(ctx, name, s), nil
	}
	return nil, fmt.Errorf("a %s connector has no contract test", c.Kind)
}

func describe(r Result) string {
	var parts []string
	for name, v := range r {
		switch {
		case v.File != nil:
			parts = append(parts, fmt.Sprintf("%s, %s, %d KB", name, v.File.Type, v.File.Size>>10))
		default:
			parts = append(parts, fmt.Sprintf("%s: %s", name, clip(v.Text, 80)))
		}
	}
	return strings.Join(parts, "; ")
}
