//go:build !unix

package diskguard

// statSpace cannot say on this platform, so nothing is refused for space.
func statSpace(string) (Space, bool) { return Space{}, false }
