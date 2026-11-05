package orchestrate

// ✨ Get assistance with a collection's description: the shared assist
// window (uiOpenAssist) talking to core/assist, which owns the model, the
// tool loop, web lookups and the reply contract. This file says only what
// the work is: what a description is for, and what the collection holds.
// It replaced a one-shot "Suggest with AI" that wrote a description and
// could not be steered; its suggestion is now the conversation's opening
// request when the description is empty.

import (
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/assist"
)

// handleCollectionAssist is POST /api/collections/{id}/assist (GET says
// whether help is available). The route's gate has checked who is asking.
func (T *OrchestrateApp) handleCollectionAssist(w http.ResponseWriter, r *http.Request, c Collection) {
	assist.Handler(assist.Spec{
		Route: "app.knowledge.description_assist", Label: "Knowledge: collection description assist", App: "/knowledge",
		Core: &T.AppCore, Think: assist.Off, Web: true,
		Allow: func(http.ResponseWriter, *http.Request) bool { return true },
		About: func(*http.Request) (string, error) {
			return collectionAssistAbout(c, T.collectionDocTitles(c, 12)), nil
		},
	})(w, r)
}

// collectionAssistAbout is what the assist is told the work is: the
// description of this collection, what one is for, and what is in it.
func collectionAssistAbout(c Collection, titles []string) string {
	var b strings.Builder
	b.WriteString("The draft is the description of a knowledge collection: a named bundle of documents that agents search when they answer.\n\n")
	b.WriteString("A description has three jobs. It says what the collection is FOR, to whoever attaches it to an agent later. " +
		"It steers Auto-fill, which searches the web for documents to add from the description's words, so the more specific it is, the better the searches. " +
		"And it stays short: one to three sentences, about 200 characters.\n\n")
	b.WriteString("Concrete nouns and named sources make a good one: \"Official Kubernetes API reference, kubectl command guide, and CNCF operator best practices\" " +
		"works far better than \"Things about kubernetes\". When unsure, name the kinds of source material it should hold. " +
		"The description is plain text: no headings, no quotes around it.\n\n")
	b.WriteString("Collection name: " + c.Name + "\n")
	if len(titles) == 0 {
		b.WriteString("It holds no documents yet.\n")
	} else {
		b.WriteString("Documents in it so far:\n- " + strings.Join(titles, "\n- ") + "\n")
	}
	return b.String()
}

// collectionDocTitles is the first n distinct section titles of the
// collection's documents, to tell the assist what is in it.
func (T *OrchestrateApp) collectionDocTitles(c Collection, n int) []string {
	var titles []string
	seen := map[string]bool{}
	prefix := collectionSource(c.ID)
	for _, ch := range ChunksWhere(T.collectionDB(c), func(x EmbeddedChunk) bool { return strings.HasPrefix(x.Source, prefix) }) {
		t := strings.TrimSpace(strings.TrimPrefix(ch.Section, "## "))
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		titles = append(titles, t)
		if len(titles) >= n {
			break
		}
	}
	return titles
}
