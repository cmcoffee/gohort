// The one hub service this leaf needs: an ID generator. Wired by core so the
// IDs here are the same shape as everywhere else in the app.

package injection

// NewID returns a fresh unique ID. Wired by core to UUIDv4.
var NewID = func() string { return "" }
