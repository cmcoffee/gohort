package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Every chat panel that sends can answer an approval card: it names a
// ConfirmURL, or it is handed to an AppChat's Panel, which fills one in. A
// panel without one draws the card and nothing happens when Approve is
// pressed: Builder opened from its dashboard card asked to change an app and
// could not be told yes, and an app's own chat could not run an action that
// asks first.
func TestEveryChatPanelCanAnswerAnApprovalCard(t *testing.T) {
	open := regexp.MustCompile(`AgentLoopPanel\{`)
	viaAppChat := regexp.MustCompile(`\.Panel\(\s*(ui\.)?$`)
	for _, root := range []string{"apps", "core"} {
		filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			src, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			s := string(src)
			for _, at := range open.FindAllStringIndex(s, -1) {
				body := literalBody(s, at[1])
				if !strings.Contains(body, "SendURL") || strings.Contains(body, "ConfirmURL") {
					continue
				}
				if viaAppChat.MatchString(strings.TrimSuffix(s[:at[0]], "ui.")) {
					continue
				}
				line := strings.Count(s[:at[0]], "\n") + 1
				t.Errorf("%s:%d: a chat panel that sends has no ConfirmURL, so an approval card in it cannot be answered", path, line)
			}
			return nil
		})
	}
}

// literalBody is the text of the composite literal whose opening brace ends
// at start, up to its matching close.
func literalBody(s string, start int) string {
	depth := 1
	for i := start; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start:i]
			}
		}
	}
	return s[start:]
}
