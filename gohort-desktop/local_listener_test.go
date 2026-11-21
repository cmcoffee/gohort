package main

// The window is served on the port the last launch used, so the page keeps
// its origin and what it stores on this device; a port taken meanwhile is
// replaced by a free one, recorded for the next launch.

import (
	"net"
	"testing"
)

func TestTheWindowKeepsItsPortAcrossLaunches(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	t.Setenv("AppData", dir)
	port := func(l net.Listener) int { return l.Addr().(*net.TCPAddr).Port }

	first, err := listenLocal()
	if err != nil {
		t.Fatal(err)
	}
	p1 := port(first)
	first.Close()

	again, err := listenLocal()
	if err != nil {
		t.Fatal(err)
	}
	if port(again) != p1 {
		t.Errorf("the next launch is on port %d, not the last launch's %d", port(again), p1)
	}

	// Still held (another program took it): a new port, recorded.
	moved, err := listenLocal()
	if err != nil {
		t.Fatal(err)
	}
	p2 := port(moved)
	if p2 == p1 {
		t.Fatalf("the port in use was handed out again")
	}
	again.Close()
	moved.Close()
	next, err := listenLocal()
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	if port(next) != p2 {
		t.Errorf("after moving to %d, the next launch is on %d", p2, port(next))
	}
}
