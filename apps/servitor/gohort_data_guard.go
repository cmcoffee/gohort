package servitor

// A system that is the machine gohort runs on is not a way into gohort's
// own data.
//
// An agent asked to clear a bulletin board it could not post to went
// looking for the board on disk: it asked a Servitor system on the gohort
// host whether boards were files, and then to read a file in gohort's data
// folder. Commands on that system run as an operating-system user that can
// read everything gohort stores, its database included, and a read is the
// one kind of command the risk gate lets through without asking. So an
// agent that may not see a record in gohort could read it through the
// machine gohort runs on.
//
// The guard refuses, before the risk gate and whatever has been granted, any
// command that names gohort's data folder (or its database file) on a system
// that is this machine: a command-type system, which runs here, or an SSH
// system whose host is this machine's name or one of its addresses. It works
// on what the command line says, so it stops an agent that has been told or
// has found the path; a command that builds the path piece by piece is not
// caught. Keeping a system's user out of the folder at the operating-system
// level is what closes that.

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// gohortDBFile is the database file's name in the data folder (APPNAME.db,
// named in the main package), caught on its own for a command that has
// already changed into the folder.
const gohortDBFile = "gohort.db"

// gohortDataPaths is gohort's data folder, as configured and with symlinks
// resolved, and the database file's name. The folder is the parent of the
// workspaces folder, which is the one path core is told at start-up.
func gohortDataPaths() (dirs []string, dbFile string) {
	ws := strings.TrimSpace(WorkspacesDir())
	if ws == "" {
		return nil, ""
	}
	dir := filepath.Clean(filepath.Dir(ws))
	if dir == "/" || dir == "." {
		return nil, ""
	}
	dirs = append(dirs, dir)
	if real, err := filepath.EvalSymlinks(dir); err == nil && real != dir {
		dirs = append(dirs, filepath.Clean(real))
	}
	return dirs, gohortDBFile
}

// namesGohortData reports whether cmd names gohort's data folder (the folder
// itself or anything under it) or its database file.
func namesGohortData(cmd string) bool {
	dirs, dbFile := gohortDataPaths()
	for _, d := range dirs {
		for i := strings.Index(cmd, d); i >= 0; {
			end := i + len(d)
			if end == len(cmd) || strings.ContainsRune("/ \t\n'\";|&)><*", rune(cmd[end])) {
				return true
			}
			next := strings.Index(cmd[end:], d)
			if next < 0 {
				break
			}
			i = end + next
		}
	}
	return dbFile != "" && strings.Contains(cmd, dbFile)
}

// isThisMachine reports whether a system runs its commands on the machine
// gohort runs on.
func isThisMachine(app Appliance) bool {
	if strings.TrimSpace(app.PeerName) != "" {
		return false // another gohort's machine
	}
	switch app.Type {
	case "command":
		return true
	case "", "ssh":
		return hostIsThisMachine(app.Host)
	}
	return false
}

// localHostCache keeps each host's answer briefly: a lookup per command
// would put a DNS round trip in front of every read.
var localHostCache sync.Map // host -> localHostAnswer

type localHostAnswer struct {
	local bool
	at    time.Time
}

// hostIsThisMachine reports whether host names this machine: localhost, its
// hostname, or an address that is one of its own.
func hostIsThisMachine(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if host == "" {
		return false
	}
	if v, ok := localHostCache.Load(host); ok {
		if a := v.(localHostAnswer); time.Since(a.at) < 5*time.Minute {
			return a.local
		}
	}
	local := lookupIsThisMachine(host)
	localHostCache.Store(host, localHostAnswer{local: local, at: time.Now()})
	return local
}

func lookupIsThisMachine(host string) bool {
	if host == "localhost" {
		return true
	}
	// Its hostname, short or full: "build01" and "build01.example.lan"
	// are this machine when either is what it calls itself.
	if name, err := os.Hostname(); err == nil {
		name = strings.ToLower(name)
		short := func(s string) string { return strings.SplitN(s, ".", 2)[0] }
		if host == name || (!strings.Contains(host, ".") || !strings.Contains(name, ".")) && short(host) == short(name) {
			return true
		}
	}
	own := map[string]bool{}
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok {
				own[ipn.IP.String()] = true
			}
		}
	}
	ips := []string{host}
	if net.ParseIP(host) == nil {
		resolved, err := net.LookupHost(host)
		if err != nil {
			return false
		}
		ips = resolved
	}
	for _, s := range ips {
		ip := net.ParseIP(s)
		if ip == nil {
			continue
		}
		if ip.IsLoopback() || own[ip.String()] {
			return true
		}
	}
	return false
}

// gohortDataRefusal is "" when cmd may run on app, or the refusal: the
// command names gohort's own data on the machine gohort runs on.
func gohortDataRefusal(app Appliance, cmd string) error {
	if !namesGohortData(cmd) || !isThisMachine(app) {
		return nil
	}
	Log("[servitor] refused a command naming gohort's data folder on %s (this machine): %q", applianceLabel(app.Name, app.ID), cmd)
	return fmt.Errorf("NOT RUN: %s is the machine gohort runs on, and this command reaches into gohort's own data folder. "+
		"gohort's stored data (its database, other agents' workspaces, boards, settings) is not read through a system, whatever this run is allowed: "+
		"use gohort's own tools for it, or tell the owner what you need. Do not look for another path to the same files", applianceLabel(app.Name, app.ID))
}
