package scribe

// A brief written from the document itself.
//
// A page taken in from somewhere else (page_import.go) arrives with no brief,
// so the Author, the Curator and Edit to the brief have nothing to keep new
// writing in line with what is there. Read from the document as it stands,
// the brief says what it already is: what it is for, what belongs in it, how
// it is organized and how it is written. The owner edits it from there like
// any brief.

import (
	"context"
	"errors"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// briefDraftPrompt asks for the brief a document already follows.
const briefDraftPrompt = `You write the writing brief for a document someone has just brought in to keep and maintain. A writing brief tells whoever writes in the document from now on, people and AI writers alike, what the document is for and how it is written, so that new material fits what is already there.

Read the document and write its brief:
- What it is for, who reads it, and what they come to it to do.
- What belongs in it and what does not.
- How it is organized: its sections, and what each kind of section holds.
- How it is written: voice and tone, level of detail, and the conventions it keeps for commands, warnings, names and steps.

Describe the document as it is. Do not suggest improvements. Write plain sentences and short lists, at most 250 words. Reply with the brief only.`

// briefDocBudget bounds how much of the document the brief is written from.
// Past it, the rest of the sections are named rather than given: how a
// document is written shows in its first stretch, and its outline in its
// headings.
const briefDocBudget = 48000

// briefSource is g as the brief is written from: its title, then its
// sections in order, as far as the budget goes, then the titles of the rest.
func briefSource(g Guide) string {
	var b strings.Builder
	b.WriteString("# " + g.Title + "\n\n")
	var rest []string
	for _, s := range g.Sections {
		part := "## " + s.Title + "\n\n" + strings.TrimSpace(s.Markdown) + "\n\n"
		if len(rest) == 0 && b.Len()+len(part) <= briefDocBudget {
			b.WriteString(part)
			continue
		}
		rest = append(rest, s.Title)
	}
	if len(rest) > 0 {
		b.WriteString("(The document continues with these sections, not shown: " + strings.Join(rest, "; ") + ".)\n")
	}
	return b.String()
}

// draftBrief writes g's brief from g.
func (T *Scribe) draftBrief(ctx context.Context, g Guide) (string, error) {
	if len(g.Sections) == 0 {
		return "", errors.New("the guide has nothing in it to write a brief from")
	}
	resp, err := T.WorkerChat(ctx, []Message{{Role: "user", Content: briefSource(g)}},
		WithSystemPrompt(briefDraftPrompt), WithCaller("scribe.brief_draft"), WithThink(false))
	if err != nil {
		return "", err
	}
	brief := strings.TrimSpace(resp.Content)
	if brief == "" {
		return "", errors.New("the model wrote nothing")
	}
	if len(brief) > guideBriefMax {
		brief = strings.TrimSpace(brief[:guideBriefMax])
	}
	return brief, nil
}
