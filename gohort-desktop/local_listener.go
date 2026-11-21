package main

import (
	"fmt"
	"net"

	"github.com/cmcoffee/gohort/gohort-desktop/core"
)

// listenLocal opens the window's loopback listener on the port the last
// launch used, so the page keeps its origin, and with it what it stores per
// device (read-back of dictated replies, a remembered tab), from one launch
// to the next. When that port is taken, or none is recorded, it takes a free
// one and records it for next time. Who may use the listener is the launch
// guard's job (launch_guard.go), not the port's: a known port gives a page
// that finds it nothing.
func listenLocal() (net.Listener, error) {
	if p := core.ReadViewerPortSidecar(); p > 0 {
		if l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p)); err == nil {
			return l, nil
		}
		core.Log("[gohort-desktop] port %d from the last launch is in use; the page starts on a new one and forgets what it kept on this device", p)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	if err := core.WriteViewerPortSidecar(l.Addr().(*net.TCPAddr).Port); err != nil {
		core.Warn("[gohort-desktop] the window's port is not recorded, so the next launch takes a new one: %v", err)
	}
	return l, nil
}
