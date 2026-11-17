package anvil

// The assistant commits. It used to be the person's alone, through the
// Commit button with a message they typed: the work was the assistant's and
// so was the knowledge of what it did, and the person was left writing the
// account of a change they had only watched. Now the assistant commits a
// finished piece of work with a message saying what changed and why, as the
// person (the history is theirs), and the commit shows in the conversation
// as a card with Undo, which takes the commit back and leaves the changes
// in place. It never pushes: Publish stays the person's.
//
// An app project's commits are its installs, so there the assistant writes
// the message instead (install_message), and Install commits with it and
// puts it in the app's history.

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/agenthost"
	"github.com/cmcoffee/gohort/core/jsonapi"
)

// commitBlock is the card a commit the assistant made is shown on.
const commitBlock = "anvil_commit"

// commitStyle is how a commit message is written, for the assistant.
const commitStyle = "The first line says what changed, under 72 characters, in the imperative (\"Add a filter for status codes\"); then a blank line and, when it is not obvious, why, and what someone reading the history later needs to know. One commit for one piece of work, not one per file."

// commitTool commits everything that differs from the last commit, as the
// person, with the message the assistant writes.
func (c *toolCtx) commitTool() AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name: "commit",
			Description: "Commit everything that differs from the last commit, with your message, as the person you are working for. Commit when a piece of work is done and its check passes, not after every edit. " +
				commitStyle + " The commit stays in this checkout: it is never pushed, and the person can undo it from its card.",
			Parameters: map[string]ToolParam{
				"message": {Type: "string", Description: "The commit message: a subject line, then optionally a blank line and a body."},
			},
			Required: []string{"message"},
			Caps:     []Capability{CapWrite},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			if why := commitRefusal(c.project, c.user, c.isAdmin); why != "" {
				return "", fmt.Errorf("%s", why)
			}
			msg, err := commitMessage(strArg(args, "message"))
			if err != nil {
				return "", fmt.Errorf("a commit needs a message of up to %d characters", maxCommitMessage)
			}
			line, n := pendingLine(c.base)
			if n == 0 {
				return "", fmt.Errorf("nothing differs from the last commit, so there is nothing to commit")
			}
			if err := commitCheckout(c.base, c.user, msg); err != nil {
				return "", fmt.Errorf("the commit failed: %w", err)
			}
			hash, _ := runGit(c.base, "rev-parse", "HEAD")
			hash = strings.TrimSpace(hash)
			subject, body, _ := strings.Cut(msg, "\n")
			if c.emitBlock != nil && hash != "" {
				c.emitBlock(agenthost.Block{
					Type:  commitBlock,
					ID:    "anvil-commit-" + hash,
					Title: strings.TrimSpace(subject),
					Text:  line,
					Data: map[string]string{
						"hash": hash, "project_id": c.project.ID,
						"subject": strings.TrimSpace(subject), "body": strings.TrimSpace(body), "files": line,
					},
				})
			}
			return fmt.Sprintf("Committed %s: %s (%s). It stays in this checkout; publishing is the person's.", shortHash(hash), strings.TrimSpace(subject), line), nil
		},
	}
}

// shortHash is a commit's id as git shows it in a log line.
func shortHash(h string) string {
	if len(h) > 8 {
		return h[:8]
	}
	return h
}

// handleCommitUndo takes back the commit a card names, when it is still the
// last one, leaving its changes in the checkout as uncommitted.
func (T *Anvil) handleCommitUndo(c *jsonapi.Call, in struct {
	Hash string `json:"hash"`
}) (any, error) {
	p, err := T.projectOf(c)
	if err != nil {
		return nil, err
	}
	if why := commitRefusal(p, c.User, callerIsAdmin(c.R)); why != "" {
		return nil, jsonapi.Forbidden(why)
	}
	base, err := p.baseDir()
	if err != nil {
		return nil, jsonapi.BadRequest(err.Error())
	}
	head, _ := runGit(base, "rev-parse", "HEAD")
	if want := strings.TrimSpace(in.Hash); want == "" || strings.TrimSpace(head) != want {
		return nil, jsonapi.Status(http.StatusConflict, "this is no longer the last commit: undo the ones after it first")
	}
	if _, err := runGit(base, "rev-parse", "--verify", "-q", "HEAD^"); err == nil {
		_, err = runGit(base, "reset", "-q", "--soft", "HEAD^")
		if err != nil {
			return nil, jsonapi.Status(http.StatusInternalServerError, err.Error())
		}
	} else if _, err := runGit(base, "update-ref", "-d", "HEAD"); err != nil {
		// The repository's first commit: the branch goes back to having none.
		return nil, jsonapi.Status(http.StatusInternalServerError, err.Error())
	}
	return map[string]any{"ok": true, "message": "Undone: its changes are back as uncommitted, to commit again."}, nil
}

// --- an app project's install message -----------------------------------------

// installMessageTable keeps the message for an app project's next install,
// by project id, in the person's store.
const installMessageTable = "anvil_install_message"

// installMessageTool writes the message the next Install commits with.
func (c *toolCtx) installMessageTool() AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name: "install_message",
			Description: "Write the commit message for your changes to this app. When the person installs them (the Install button, or as the turn ends with Install on every save), the install is committed with it and it goes in the app's history as the revision's note. " +
				commitStyle + " Write it when a piece of work is done; writing it again replaces it, so it covers everything since the last install.",
			Parameters: map[string]ToolParam{
				"message": {Type: "string", Description: "The commit message: a subject line, then optionally a blank line and a body."},
			},
			Required: []string{"message"},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			msg, err := commitMessage(strArg(args, "message"))
			if err != nil {
				return "", fmt.Errorf("a message of up to %d characters", maxCommitMessage)
			}
			if c.udb == nil {
				return "", fmt.Errorf("the message cannot be kept here")
			}
			c.udb.Set(installMessageTable, c.project.ID, msg)
			subject, _, _ := strings.Cut(msg, "\n")
			return fmt.Sprintf("Kept for the next install: %q.", strings.TrimSpace(subject)), nil
		},
	}
}

// installMessage is the message waiting for p's next install, or "".
func installMessage(udb Database, projectID string) string {
	var msg string
	if udb != nil {
		udb.Get(installMessageTable, projectID, &msg)
	}
	return strings.TrimSpace(msg)
}
