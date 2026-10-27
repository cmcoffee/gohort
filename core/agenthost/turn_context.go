package agenthost

import (
	"context"
	"net/http"
)

// turnContextKey carries an app's turn context on a send request.
type turnContextKey struct{}

// WithTurnContext is r carrying text for this turn's model and nothing else:
// what the host app knows about the moment the message was sent (an editor's
// unsaved contents, the file it is on), which the person did not type and the
// conversation does not keep. The host puts it on the send it hands the
// agent; the turn adds it to the newest user message, for this run only.
//
// The newest message, not the system prompt: it changes every turn, and a
// system prompt that changes re-reads the whole conversation every turn. Not
// stored: the next turn carries its own, and a conversation that kept every
// copy would grow by the whole file each turn and answer from old ones.
func WithTurnContext(r *http.Request, text string) *http.Request {
	if text == "" {
		return r
	}
	return r.WithContext(context.WithValue(r.Context(), turnContextKey{}, text))
}

// TurnContext is the text WithTurnContext put on ctx, or "".
func TurnContext(ctx context.Context) string {
	s, _ := ctx.Value(turnContextKey{}).(string)
	return s
}
