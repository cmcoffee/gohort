// Searching files in parallel, answering in file order.
//
// A search is CPU-bound: matching, dating lines, decompressing a .gz,
// decrypting a bundle slice. One goroutine walking the files used one core of
// however many the host has, pinned while the rest sat idle. Files are
// independent, so they are scanned several at once, each on its own (counting
// every match, keeping its share in order, grouping and dating into its own
// structures), and the results are merged strictly in file order. Every limit
// that depends on what came before (the search-wide keep limit, the total, the
// cut notes) is applied in that merge, so the answer is the one a single
// goroutine would have given; the only difference is that a few files past
// the point where the limit filled may already have been read when it does.
package bundle

import (
	"context"
	"runtime"

	"github.com/cmcoffee/snugforge/xsync"
)

// SearchWorkers is how many files one search reads at once: one fewer than
// the host's cores, so a search never takes the whole machine from the turn
// it is part of, and at most eight. One means a plain serial walk. A variable
// so a test can compare the two.
var SearchWorkers = defaultSearchWorkers()

func defaultSearchWorkers() int {
	n := runtime.NumCPU() - 1
	if n > 8 {
		n = 8
	}
	if n < 1 {
		n = 1
	}
	return n
}

// EachInOrder scans n files, up to SearchWorkers at once, and hands each
// result to merge in file order. merge returning false ends the run: scans in
// flight see their context cancelled, and EachInOrder waits for them, so no
// scan outlives the search.
//
// Only a window of files ahead of the merge is started, so a slow file holds
// back a bounded number of finished ones rather than the whole folder's.
func EachInOrder[T any](ctx context.Context, n int, scan func(ctx context.Context, i int) T, merge func(i int, r T) bool) {
	workers := SearchWorkers
	if workers <= 1 || n <= 1 {
		for i := 0; i < n; i++ {
			if !merge(i, scan(ctx, i)) {
				return
			}
		}
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	lg := xsync.NewLimitGroup(workers)
	defer func() {
		cancel()
		lg.Wait()
	}()
	window := workers * 2
	pending := make([]chan T, n)
	next := 0
	for i := 0; i < n; i++ {
		for next < n && next < i+window {
			j := next
			ch := make(chan T, 1) // buffered: an abandoned scan never blocks on its send
			pending[j] = ch
			lg.Add(1)
			go func() {
				defer lg.Done()
				ch <- scan(ctx, j)
			}()
			next++
		}
		r := <-pending[i]
		pending[i] = nil
		if !merge(i, r) {
			return
		}
	}
}
