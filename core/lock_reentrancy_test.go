package core

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// tempToolPersistMu is process-global and non-reentrant, so re-locking it
// while held blocks that goroutine FOREVER while it still owns the lock —
// freezing tool persistence for every user, not just the caller.
//
// This has now happened twice in this file:
//
//	ApprovePendingTempTool -> RemoveSessionTempTool                  (direct)
//	AdminPersistTempTool   -> cleanupSessionDraftsByName -> Remove…  (transitive)
//
// The second survived a one-level audit because the middle function does not
// lock. So this check is TRANSITIVE: it walks the call graph from every
// lock-holding function through non-locking helpers, and fails if any path
// reaches a function that locks again.
//
// Convention that keeps it satisfiable: a helper meant to be called under the
// lock ends in "Locked" and never locks itself.
func TestNoReentrantToolPersistLock(t *testing.T) {
	src, err := os.ReadFile("temp_tool_persist.go")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	body := string(src)

	type fn struct {
		name  string
		body  string
		locks bool
	}
	funcRe := regexp.MustCompile(`(?m)^func (?:\([^)]*\)\s*)?([A-Za-z0-9_]+)\(`)
	locs := funcRe.FindAllStringSubmatchIndex(body, -1)
	funcs := map[string]*fn{}
	var order []string
	for i, m := range locs {
		name := body[m[2]:m[3]]
		end := len(body)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		b := body[m[0]:end]
		funcs[name] = &fn{name: name, body: b, locks: strings.Contains(b, "tempToolPersistMu.Lock()")}
		order = append(order, name)
	}

	calls := func(f *fn, callee string) bool {
		// Crude call detection, deliberately: a false positive costs one
		// rename, a false negative costs a process-wide freeze.
		idx := strings.Index(f.body, callee+"(")
		for idx >= 0 {
			if idx == 0 || (!isLockScanIdentByte(f.body[idx-1]) && f.body[idx-1] != '.') {
				return true
			}
			next := strings.Index(f.body[idx+1:], callee+"(")
			if next < 0 {
				return false
			}
			idx += 1 + next
		}
		return false
	}

	// Depth-first from each locking function; report any path that re-locks.
	var bad []string
	for _, start := range order {
		f := funcs[start]
		if !f.locks {
			continue
		}
		seen := map[string]bool{start: true}
		var walk func(cur *fn, path []string)
		walk = func(cur *fn, path []string) {
			for _, name := range order {
				callee := funcs[name]
				if callee == nil || name == cur.name || seen[name] {
					continue
				}
				if !calls(cur, name) {
					continue
				}
				if callee.locks {
					bad = append(bad, strings.Join(append(path, name), " -> "))
					continue
				}
				seen[name] = true
				walk(callee, append(path, name))
			}
		}
		walk(f, []string{start})
	}
	if len(bad) > 0 {
		t.Errorf("re-entrant tempToolPersistMu acquisition — the holder blocks forever AND keeps the global lock, freezing tool persistence process-wide.\n"+
			"Call the lock-free \"…Locked\" core instead:\n  %s", strings.Join(bad, "\n  "))
	}
}

func isLockScanIdentByte(c byte) bool {
	return c == '_' ||
		(c >= 'a' && c <= 'z') ||
		(c >= 'A' && c <= 'Z') ||
		(c >= '0' && c <= '9')
}
