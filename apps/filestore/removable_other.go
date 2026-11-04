//go:build !unix

package filestore

// cleanupBlocker cannot be answered ahead of time off Unix: Windows decides
// deletion by ACLs that a mode check does not read. The removal itself still
// reports what it could not remove.
func cleanupBlocker(root string) string { return "" }

// removeBlocker: see cleanupBlocker.
func removeBlocker(dir string) string { return "" }
