//go:build windows

package safefile

// noFollow is nothing on Windows, where creating a link takes a privilege a
// sandboxed script does not have; Create still removes one it finds.
const noFollow = 0
