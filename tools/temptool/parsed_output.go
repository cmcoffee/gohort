package temptool

// Output a caller parses, not a model reads.
//
// A shell tool's output is trimmed to maxOutput for the model that called it,
// with a note saying so. An app's data source or action prints its reply as
// JSON (tools/appscript), and that reply is read by the app, its page, and the
// MCP and HTTP clients calling it, never shown to a model as it is (an agent
// calling an app's tool has its result capped by its own loop). Trimmed, the
// reply was cut mid-value: a table broke once its app held a few hundred
// records, and the error said the script printed no JSON. A run marked here
// keeps its output whole up to ParsedOutputMax, and one that prints more fails
// saying how much it printed.

import (
	"context"
	"fmt"

	. "github.com/cmcoffee/gohort/core"
)

// ParsedOutputMax is how much a run whose output is parsed may print: the
// same 8 MB a background run keeps in memory (backgroundOutputKeep).
const ParsedOutputMax = backgroundOutputKeep

type parsedOutputKey struct{}

// WithParsedOutput marks ctx's runs as ones whose output the caller parses.
func WithParsedOutput(ctx context.Context) context.Context {
	return context.WithValue(ctx, parsedOutputKey{}, true)
}

// parsedOutput reports whether ctx's runs are marked (WithParsedOutput).
func parsedOutput(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	on, _ := ctx.Value(parsedOutputKey{}).(bool)
	return on
}

// tooMuchParsedOutput is the failure of a marked run that printed more than
// ParsedOutputMax.
func tooMuchParsedOutput(total int64) error {
	return fmt.Errorf("the script printed %s, more than the %s a script's reply may be: print less (a page of the records, a summary, the fields the page shows) or keep the rest in a file", HumanSize(total), HumanSize(ParsedOutputMax))
}
