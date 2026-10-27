package admin

import (
	"testing"
)

// A new Bedrock setup starts on Converse; a tier already on Bedrock with the
// historical blank keeps the Messages API it has been running.
func TestBedrockAPIShown(t *testing.T) {
	for _, c := range []struct{ provider, stored, want string }{
		{"", "", "converse"},
		{"vllm", "", "converse"},
		{"bedrock", "", "messages"},
		{"bedrock", "invoke", "invoke"},
		{"bedrock", "converse", "converse"},
	} {
		if got := bedrockAPIShown(c.provider, c.stored); got != c.want {
			t.Errorf("bedrockAPIShown(%q, %q) = %q, want %q", c.provider, c.stored, got, c.want)
		}
	}
	if bedrockUsesMantle("converse") || bedrockUsesMantle("invoke") || !bedrockUsesMantle("") || !bedrockUsesMantle("messages") {
		t.Error("the mantle endpoint check applies to the wrong modes")
	}
}
