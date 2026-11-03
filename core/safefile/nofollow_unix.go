//go:build !windows

package safefile

import "syscall"

// noFollow refuses a link at the path, and does not wait on a FIFO's other
// end before regular() can turn it away.
const noFollow = syscall.O_NOFOLLOW | syscall.O_NONBLOCK
