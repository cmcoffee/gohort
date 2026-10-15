package bgrun

// Whether a running command is doing anything, from the process itself.
//
// Output and growing files are what a person can see, and they are what the
// no-output limit read. A command can be busy with neither: a download
// receiving into a temporary file somewhere else, a transfer verifying what it
// fetched, an unpacker reading a large archive before it writes. Those were
// stopped as stuck while they worked. The process says it is working in a way
// those cannot hide: its CPU time, and on Linux the bytes it reads and writes,
// keep moving. A process blocked on nothing moves neither, and is still
// stopped at the limit.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// WatchProcess counts pid (and its children) as activity while it uses CPU or
// moves data, until ctx ends. Call it once the process has started.
func (a *RunActivity) WatchProcess(ctx context.Context, pid int) {
	if a == nil || pid <= 0 {
		return
	}
	go func() {
		tick := time.NewTicker(growthCheckEvery)
		defer tick.Stop()
		var last uint64
		seen := false
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				n, ok := processWork(pid)
				if !ok {
					continue
				}
				if seen && n > last {
					a.mu.Lock()
					a.last = time.Now()
					a.mu.Unlock()
				}
				last, seen = n, true
			}
		}
	}()
}

// processWork is a number that grows while pid or its children work: CPU
// ticks, plus bytes read and written where the system reports them.
func processWork(pid int) (uint64, bool) {
	if runtime.GOOS == "linux" {
		return procWork(pid)
	}
	return psWork(pid)
}

// procWork reads /proc: utime+stime of pid and its direct children, and the
// characters they read and wrote (rchar, wchar), which count a socket read
// through read() as well as a file's.
func procWork(pid int) (uint64, bool) {
	pids := []int{pid}
	if entries, err := os.ReadDir("/proc"); err == nil {
		for _, e := range entries {
			child, err := strconv.Atoi(e.Name())
			if err != nil || child == pid {
				continue
			}
			if ppid, _, ok := procStat(child); ok && ppid == pid {
				pids = append(pids, child)
			}
		}
	}
	var total uint64
	found := false
	for _, p := range pids {
		if _, cpu, ok := procStat(p); ok {
			total += cpu
			found = true
		}
		if b, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(p), "io")); err == nil {
			for _, line := range strings.Split(string(b), "\n") {
				k, v, ok := strings.Cut(line, ":")
				if !ok || (k != "rchar" && k != "wchar") {
					continue
				}
				if n, err := strconv.ParseUint(strings.TrimSpace(v), 10, 64); err == nil {
					total += n
				}
			}
		}
	}
	return total, found
}

// procStat is a process's parent and its utime+stime in clock ticks.
func procStat(pid int) (ppid int, cpu uint64, ok bool) {
	b, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0, 0, false
	}
	// The command name is in parentheses and may hold spaces; the fields
	// after the last ')' are fixed: state, ppid, ... utime is the 12th of
	// them and stime the 13th.
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0, 0, false
	}
	f := strings.Fields(s[i+1:])
	if len(f) < 13 {
		return 0, 0, false
	}
	ppid, _ = strconv.Atoi(f[1])
	ut, _ := strconv.ParseUint(f[11], 10, 64)
	st, _ := strconv.ParseUint(f[12], 10, 64)
	return ppid, ut + st, true
}

// psWork asks ps for the CPU time of pid and its direct children, in
// hundredths of a second, where there is no /proc (macOS).
func psWork(pid int) (uint64, bool) {
	out, err := exec.Command("ps", "-A", "-o", "pid=,ppid=,time=").Output()
	if err != nil {
		return 0, false
	}
	var total uint64
	found := false
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		p, _ := strconv.Atoi(f[0])
		pp, _ := strconv.Atoi(f[1])
		if p != pid && pp != pid {
			continue
		}
		total += psTime(f[2])
		found = found || p == pid
	}
	return total, found
}

// psTime reads ps's cputime, "[[dd-]hh:]mm:ss[.cc]", in hundredths of a
// second.
func psTime(s string) uint64 {
	days := uint64(0)
	if d, rest, ok := strings.Cut(s, "-"); ok {
		days, _ = strconv.ParseUint(d, 10, 64)
		s = rest
	}
	frac := uint64(0)
	if whole, cc, ok := strings.Cut(s, "."); ok {
		frac, _ = strconv.ParseUint((cc + "00")[:2], 10, 64)
		s = whole
	}
	var secs uint64
	for _, part := range strings.Split(s, ":") {
		n, _ := strconv.ParseUint(part, 10, 64)
		secs = secs*60 + n
	}
	return (days*86400+secs)*100 + frac
}
