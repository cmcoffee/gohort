package service

import (
	"context"
	"fmt"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// The free check: each approved service with a health operation is asked it
// every every_mins, a call the provider does not charge for, that proves the
// service is up and its key accepted. Never a capability's real call: that
// costs money, and runs only as a contract test someone asked for.

var healthOnce sync.Once

func startHealth() {
	healthOnce.Do(func() {
		go func() {
			t := time.NewTicker(time.Minute)
			defer t.Stop()
			for range t.C {
				checkDue()
			}
		}()
	})
}

func checkDue() {
	if RootDB == nil {
		return
	}
	for _, c := range ListConnectors(RootDB) {
		if c.Kind != Kind || !c.Approved {
			continue
		}
		s, err := specOf(c)
		if err != nil || s.Provider.Health == nil {
			continue
		}
		every := time.Duration(s.Provider.Health.EveryMins) * time.Minute
		if every == 0 {
			every = defaultHealthMins * time.Minute
		}
		if h := RecordOf(c.Name).Health; h != nil && time.Since(h.At) < every {
			continue
		}
		checkHealth(context.Background(), c.Name, s)
	}
}

// CheckHealth runs a connector's free check now and records it. A connector
// with no health operation says so: it is known only from its calls and its
// contract tests, never shown healthy.
func CheckHealth(ctx context.Context, name string) (HealthCheck, error) {
	c, ok := GetConnector(RootDB, name)
	if !ok || c.Kind != Kind {
		return HealthCheck{}, fmt.Errorf("no service connector named %q", name)
	}
	s, err := specOf(c)
	if err != nil {
		return HealthCheck{}, err
	}
	if s.Provider.Health == nil {
		return HealthCheck{}, fmt.Errorf("%s declares no free health check, so it is known only from its calls and its contract tests", name)
	}
	return checkHealth(ctx, name, s), nil
}

func checkHealth(ctx context.Context, name string, s Spec) HealthCheck {
	h := s.Provider.Health
	t := transport{p: s.Provider}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	r, err := t.do(ctx, h.Call, map[string]any{})
	check := HealthCheck{At: time.Now(), OK: err == nil}
	switch {
	case err != nil:
		check.Message = clip(err.Error(), 300)
	case h.Expect != "":
		if c, _ := parseCond(h.Expect); !c.holds(r.node, nil) {
			check.OK = false
			check.Message = fmt.Sprintf("the reply does not hold %s: %s", h.Expect, clip(r.text, 200))
		}
	}
	if check.OK {
		check.Message = "answered " + h.Call
	}
	updateRecord(name, func(rec *Record) { rec.Health = &check })
	return check
}
