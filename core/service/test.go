package service

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/sourcehooks"
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

// Testable reports whether connector c has a contract test: a service, or an
// image backend of the older rest_image kind.
func Testable(c Connector) bool {
	return c.Kind == Kind || (c.Kind == RestImageConnectorKind && c.Approved)
}

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
	case RestImageConnectorKind:
		if !c.Approved {
			return nil, fmt.Errorf("approve %s first: an image backend of this kind is tested through its live registration", name)
		}
		if err := takeTest(name, Spec{}); err != nil {
			return nil, err
		}
		return []TestReport{testImageBackend(ctx, name)}, nil
	}
	return nil, fmt.Errorf("a %s connector has no contract test", c.Kind)
}

// testImageBackend tests an older image backend through its registration,
// with the image contract's own check. Its cost is billed by its credential's
// price, as its calls always are.
func testImageBackend(ctx context.Context, name string) TestReport {
	ct, _ := Lookup("image.generate")
	rep := TestReport{Capability: ct.Name}
	res, err := GenerateImageWithBackend(ctx, name, fmt.Sprint(ct.Test["prompt"]), false)
	if err != nil {
		rep.Detail = err.Error()
		return rep
	}
	path := res.URL
	if u, perr := url.Parse(res.URL); perr == nil && (u.Scheme == "http" || u.Scheme == "https") {
		if err := RefuseNonPublicHost(res.URL); err != nil {
			rep.Detail = err.Error()
			return rep
		}
		resp, err := sourcehooks.NewPublicHTTPClient().Get(res.URL)
		if err != nil {
			rep.Detail = "fetching the image: " + err.Error()
			return rep
		}
		defer resp.Body.Close()
		f, _, err := writeFile(resp.Body, defaultMaxBytes)
		if err != nil {
			rep.Detail = "fetching the image: " + err.Error()
			return rep
		}
		path = f.Path
	}
	if err := imageFileOK(path); err != nil {
		rep.Detail = "an image came back, but " + err.Error()
		return rep
	}
	rep.OK, rep.File, rep.Detail = true, path, "passed: an image came back (billed by its credential's price, if one is set)"
	return rep
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
