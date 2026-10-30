package messaging

import "testing"

// Every registered watcher hears every inbound handed to it.
func TestEveryWatcherHearsAnInbound(t *testing.T) {
	var a, b []string
	RegisterInboundWatcher(func(ev InboundEvent) {
		if ev.Owner == "watch-test" {
			a = append(a, ev.Text)
		}
	})
	RegisterInboundWatcher(func(ev InboundEvent) {
		if ev.Owner == "watch-test" {
			b = append(b, ev.Handle)
		}
	})
	RegisterInboundWatcher(nil)
	WatchInbound(InboundEvent{Owner: "watch-test", Handle: "+15550100", Text: "yes"})
	if len(a) != 1 || a[0] != "yes" || len(b) != 1 || b[0] != "+15550100" {
		t.Errorf("watchers heard %v and %v", a, b)
	}
}
