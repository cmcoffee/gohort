//go:build unix

package diskguard

import "golang.org/x/sys/unix"

func statSpace(path string) (Space, bool) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return Space{}, false
	}
	bs := uint64(st.Bsize)
	return Space{Free: uint64(st.Bavail) * bs, Total: uint64(st.Blocks) * bs}, true
}
