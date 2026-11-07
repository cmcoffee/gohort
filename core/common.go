package core

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/cmcoffee/snugforge/cfg"
	"github.com/cmcoffee/snugforge/eflag"
	"github.com/cmcoffee/snugforge/nfo"
	"github.com/cmcoffee/snugforge/swapreader"
	"github.com/cmcoffee/snugforge/xsync"
)

// AppVersion is set at startup from the build-time VERSION variable.
var AppVersion = "dev"

// err_table stores error messages for reporting.
var err_table *Table

// SetErrTable sets the error table.
func SetErrTable(input Table) {
	err_table = &input
	err_table.Drop()
}

// FlagSet encapsulates command-line flags and provides parsing functionality.
type FlagSet struct {
	FlagArgs []string
	*eflag.EFlagSet
}

// Parse parses the command-line arguments.
func (f *FlagSet) Parse() (err error) {
	if err = f.EFlagSet.Parse(f.FlagArgs[0:]); err != nil {
		return err
	}
	return nil
}

// Text returns the underlying command-line arguments.
func (f *FlagSet) Text() (output []string) {
	return f.FlagArgs
}

// NONE is an empty string.
// SLASH is the operating system's path separator.
const (
	NONE  = ""
	SLASH = string(os.PathSeparator)
)

type Options = nfo.Options

var (
	NewOptions = nfo.NewOptions
	Log        = nfo.Log // Standard Log Output
	// AuxLog writes to the log FILE only, never the terminal (init_logging routes
	// nfo.AUX2 there). Use for high-volume success noise — HTTP 2xx access lines,
	// successful fetch completions — that should stay auditable in the log but not
	// flood a terminal someone is watching. Non-success stays on Log (terminal).
	AuxLog          = nfo.Aux2
	Fatal           = nfo.Fatal           // Fatal Log Output & Exit.
	Critical        = nfo.Critical        // err-or-nil → Fatal helper
	Notice          = nfo.Notice          // Notice Log Output
	Flash           = nfo.Flash           // Flash to Stderr
	Stdout          = nfo.Stdout          // Send to Stdout
	Warn            = nfo.Warn            // Warn Log Output
	Defer           = nfo.Defer           // Global Application Defer
	Debug           = nfo.Debug           // Debug Log Output
	Trace           = nfo.Trace           // Trace Log Output
	Exit            = nfo.Exit            // End Application, Run Global Defer.
	PleaseWait      = nfo.PleaseWait      // Set Loading Prompt
	Stderr          = nfo.Stderr          // Send to Stderr
	ProgressBar     = nfo.NewProgressBar  // Set Progress Bar animation
	Path            = filepath.Clean      // Provide clean path
	TransferCounter = nfo.TransferCounter // Transfer Animation
	NewLimitGroup   = xsync.NewLimitGroup // Limiter Group
	FormatPath      = filepath.FromSlash  // Convert to standard path.
	GetPath         = filepath.ToSlash    // Convert to OS specific path.
	Info            = nfo.Aux             // Log as standard INFO
	HumanSize       = nfo.HumanSize       // Convert bytes int64 to B/KB/MB/GB/TB.
	GetInput        = nfo.GetInput        // Prompt user for text input.
)

// HumanCount formats an integer with thousands separators (12345 -> "12,345").
//
// The companion to HumanSize, which nfo supplies and this does not: a count of
// lines or files is as unreadable at seven digits as a count of bytes.
//
// core/bundle carries its own unexported copy. That is not an oversight — a
// package under core cannot import core, so the alternative to two copies is a
// third package existing for fifteen lines of formatting. The duplication is
// forced by the import direction and stops here: everything that CAN reach this
// one uses it.
func HumanCount(n int) string {
	s := fmt.Sprintf("%d", n)
	neg := ""
	if strings.HasPrefix(s, "-") {
		neg, s = "-", s[1:]
	}
	if len(s) <= 3 {
		return neg + s
	}
	var b strings.Builder
	pre := len(s) % 3
	if pre > 0 {
		b.WriteString(s[:pre])
	}
	for i := pre; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteString(",")
		}
		b.WriteString(s[i : i+3])
	}
	return neg + b.String()
}

// traceEnabled mirrors whether the TRACE sink is actually routed
// somewhere.
//
// nfo discards a Trace() call when the level is off, but only AFTER the
// arguments have been built. Wire-level snooping builds whole request
// and response bodies — an LLM call carrying 80 tool schemas is a
// ~190KB JSON document, and the snoop helpers parse it into a
// map[string]interface{} and re-serialize it indented. That is real CPU
// on every single call, thrown away, on deployments that never asked for
// tracing. The write can't be skipped from inside nfo; the FORMATTING
// has to be skipped by the caller, which needs this flag.
var traceEnabled atomic.Bool

// SetTraceEnabled tells core whether wire-level tracing is routed. Owned
// by the main package, which knows the --trace / --snoop state and where
// the TRACE sink points; core has no view of either.
func SetTraceEnabled(on bool) { traceEnabled.Store(on) }

// TraceEnabled reports whether building trace-only payloads is worth it.
// Guard any Trace() call whose ARGUMENTS cost something to produce; a
// Trace of values you already hold needs no guard.
func TraceEnabled() bool { return traceEnabled.Load() }

var (
	transferMonitor = nfo.TransferMonitor
	leftToRight     = nfo.LeftToRight
	rightToLeft     = nfo.RightToLeft
	nopSeeker       = nfo.NopSeeker
	noRate          = nfo.NoRate
)

// NewFlagSet returns a new flag set.
var (
	NewFlagSet      = eflag.NewFlagSet
	ReturnErrorOnly = eflag.ReturnErrorOnly
)

type (
	BitFlag        = xsync.BitFlag
	LimitGroup     = xsync.LimitGroup
	ConfigStore    = cfg.Store
	ReadSeekCloser = nfo.ReadSeekCloser
	SwapReader     = swapreader.Reader
)

// error_counter tracks the number of errors encountered.
var error_counter uint32

// ErrCount returns amount of times Err has been triggered.
func ErrCount() uint32 {
	return atomic.LoadUint32(&error_counter)
}

// Err logs a standard error and adds counter to ErrCount().
func Err(input ...interface{}) {
	atomic.AddUint32(&error_counter, 1)
	msg := nfo.Stringer(input...)
	nfo.Err(msg)
	if err_table != nil {
		err_table.Set(fmt.Sprintf("%d", atomic.LoadUint32(&error_counter)), fmt.Sprintf("<%v> %s", time.Now().Round(time.Second), msg))
	}
}

// DefaultPleaseWait resets the please wait prompt to its default.
func DefaultPleaseWait() {
	PleaseWait.Set(func() string { return "Please wait ..." }, []string{"[>  ]", "[>> ]", "[>>>]", "[ >>]", "[  >]", "[  <]", "[ <<]", "[<<<]", "[<< ]", "[<  ]"})
}

// UUIDv4 generates a UUID v4.
func UUIDv4() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// RandBytes generates a random byte slice of length specified.
func RandBytes(sz int) []byte {
	if sz <= 0 {
		sz = 16
	}

	ch := "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789+/"
	chlen := len(ch)

	rand_string := make([]byte, sz)
	rand.Read(rand_string)

	for i, v := range rand_string {
		rand_string[i] = ch[v%byte(chlen)]
	}
	return rand_string
}

// Error handler for const errors.
type Error string

func (e Error) Error() string { return string(e) }

// MkDir creates folders.
func MkDir(name ...string) (err error) {
	for _, path := range name {
		err = os.MkdirAll(path, 0766)
		if err != nil {
			subs := strings.Split(path, string(os.PathSeparator))
			for i := 0; i < len(subs); i++ {
				p := strings.Join(subs[0:i+1], string(os.PathSeparator))
				if p == "" {
					p = "."
				}
				f, err := os.Stat(p)
				if err != nil {
					if os.IsNotExist(err) {
						err = os.Mkdir(p, 0766)
						if err != nil && !os.IsExist(err) {
							return err
						}
					} else {
						return err
					}
				}
				if f != nil && !f.IsDir() {
					return fmt.Errorf("mkdir: %s: file exists", f.Name())
				}
			}
		}
	}
	return nil
}

// LocalPath adapts path to whatever local filesystem uses.
func LocalPath(path string) string {
	path = strings.Replace(path, "/", SLASH, -1)
	subs := strings.Split(path, SLASH)
	for i, v := range subs {
		subs[i] = strings.TrimSpace(v)
	}
	return strings.Join(subs, SLASH)
}

// Rename renames a path.
func Rename(oldpath, newpath string) error {
	return os.Rename(oldpath, newpath)
}

// IsBlank confirms all strings handed to it are empty.
func IsBlank(input ...string) bool {
	for _, v := range input {
		if len(v) == 0 {
			return true
		}
	}
	return false
}

// Delete removes a file at the given path.
func Delete(path string) error {
	return os.Remove(LocalPath(path))
}
