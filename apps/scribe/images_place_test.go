package scribe

// A picture gets into a guide two more ways than the editor: dropped on the
// rendered page, where it goes into the section it landed on, and handed to
// the co-author as media#N, the picture the user attached to the message.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cmcoffee/oddjob/apps/orchestrate"
	. "github.com/cmcoffee/oddjob/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func TestADroppedPictureGoesIntoItsSection(t *testing.T) {
	g := Guide{ID: "g1", Sections: []Section{
		{ID: "a", Title: "Setup", Order: 1, Markdown: "Install it."},
		{ID: "b", Title: "Use", Order: 2, Markdown: "Run it.\n"},
	}}
	if title := placeImageInSection(&g, "a", "![x](p)"); title != "Setup" || g.Sections[0].Markdown != "Install it.\n\n![x](p)\n" {
		t.Errorf("into Setup: %q %q", title, g.Sections[0].Markdown)
	}
	if title := placeImageInSection(&g, "", "![y](q)"); title != "Use" || !strings.HasSuffix(g.Sections[1].Markdown, "Run it.\n\n![y](q)\n") {
		t.Errorf("no section named goes to the last: %q %q", title, g.Sections[1].Markdown)
	}
	if title := placeImageInSection(&g, "nope", "![z](r)"); title != "Use" {
		t.Errorf("an unknown section goes to the last: %q", title)
	}
	empty := Guide{ID: "g2"}
	if title := placeImageInSection(&empty, "", "![x](p)"); title != "" {
		t.Errorf("a guide with no sections has nowhere to put it: %q", title)
	}
}

func TestTheCoAuthorPlacesAnAttachedPicture(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	udb := UserDB(root, "u")
	T := &Scribe{AppCore: AppCore{DB: root}}
	orch := &orchestrate.OrchestrateApp{AppCore: AppCore{DB: root}}
	saveGuideRev(udb, Guide{ID: "g1", Owner: "u", Title: "Guide", Sections: []Section{{ID: "a", Title: "Setup", Order: 1, Markdown: "Install it."}}}, "new")
	var addImage AgentToolDef
	for _, td := range T.coauthorTools(coauthorScope{Ctx: context.Background(), UDB: udb, Orch: orch, User: "u", CanEdit: true, Guide: "g1"}) {
		if td.Tool.Name == "add_image" {
			addImage = td
		}
	}
	if addImage.Handler == nil {
		t.Fatal("no add_image tool")
	}
	sess := &ToolSession{}
	sess.RegisterInboundMedia("image", tinyPNG, "")
	ctx := sess.ContextWithSession(context.Background())

	out, err := addImage.Handler(ctx, map[string]any{"section_title": "setup", "image": "media#1", "caption": "The [login] screen"})
	if err != nil {
		t.Fatalf("media#1: %v", err)
	}
	g, _ := loadGuide(udb, "g1")
	md := g.Sections[0].Markdown
	if !strings.Contains(out, "Setup") || !strings.Contains(md, "![The login screen](") || !strings.Contains(md, "/img?g=g1&i=") {
		t.Errorf("the picture did not land in Setup:\n%s\n%s", out, md)
	}
	ref := guideImageRefRE.FindStringSubmatch(md)
	var img guideImage
	if ref == nil || !udb.Get(guideImagesTable, guideImageKey(ref[1], ref[2]), &img) || img.Mime != "image/png" {
		t.Error("the attached picture was not stored with the guide")
	}

	if _, err := addImage.Handler(ctx, map[string]any{"section_title": "Setup", "image": "media#2"}); err == nil || !strings.Contains(err.Error(), "past the end") {
		t.Errorf("a media id past the end should say so: %v", err)
	}
	if _, err := addImage.Handler(context.Background(), map[string]any{"section_title": "Setup", "image": "media#1"}); err == nil || !strings.Contains(err.Error(), "outside a turn") {
		t.Errorf("outside a turn there is nothing attached: %v", err)
	}
	// A picture a tool made or found: the file the tool handed back, in the
	// turn's workspace.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "diagram.png"), tinyPNG, 0o644); err != nil {
		t.Fatal(err)
	}
	made := &ToolSession{WorkspaceDir: dir}
	if _, err := addImage.Handler(made.ContextWithSession(context.Background()), map[string]any{"section_title": "Setup", "image": "diagram.png", "caption": "Flow"}); err != nil {
		t.Errorf("a generated picture by its filename: %v", err)
	}
	g, _ = loadGuide(udb, "g1")
	if !strings.Contains(g.Sections[0].Markdown, "![Flow](/scribe/img?g=g1&i=") {
		t.Errorf("the generated picture was not stored and placed:\n%s", g.Sections[0].Markdown)
	}
	if _, err := addImage.Handler(ctx, map[string]any{"section_title": "Setup", "image": "https://example.com/a.png", "caption": "Remote"}); err != nil {
		t.Errorf("a URL is placed as it is: %v", err)
	}
	g, _ = loadGuide(udb, "g1")
	if !strings.Contains(g.Sections[0].Markdown, "![Remote](https://example.com/a.png)") {
		t.Errorf("the URL picture did not land:\n%s", g.Sections[0].Markdown)
	}
	if _, err := addImage.Handler(ctx, map[string]any{"section_title": "Nope", "image": "media#1"}); err == nil || !strings.Contains(err.Error(), "Setup") {
		t.Errorf("a missing section should list the ones there are: %v", err)
	}
}
