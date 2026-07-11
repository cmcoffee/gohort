package core

// LatestUserContent returns the content of the most recent user message, or ""
// when there is none.
func LatestUserContent(msgs []Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			return msgs[i].Content
		}
	}
	return ""
}
