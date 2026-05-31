// Logging aliases. Other packages import core and use Log / Debug /
// Err / Fatal directly — same pattern gohort's own core/common.go
// exposes for the main project. Routes everything through
// snugforge/nfo so output style + destination match the rest of the
// gohort ecosystem.

package core

import "github.com/cmcoffee/snugforge/nfo"

var (
	Log      = nfo.Log      // standard info line
	Debug    = nfo.Debug    // verbose; off by default
	Trace    = nfo.Trace    // very verbose; off by default
	Err      = nfo.Err      // recoverable error — counted, kept going
	Fatal    = nfo.Fatal    // unrecoverable — log and exit
	Critical = nfo.Critical // err-or-nil → Fatal helper
	Notice   = nfo.Notice   // user-visible heads-up
	Warn     = nfo.Warn     // soft problem worth flagging
	Stdout   = nfo.Stdout
	Stderr   = nfo.Stderr
)
