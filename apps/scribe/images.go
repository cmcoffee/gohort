// Images in a guide: screenshots and pictures pasted or dropped into the
// editor. Each is stored once, in the guide owner's store beside the guide, and
// the guide's markdown points at it (![alt](/scribe/img?g=<guide>&i=<image>)).
// Not inlined into the markdown: every revision copies the guide, so an
// embedded image would be copied into History on every edit.
//
// What leaves gohort carries the pictures with it: the export formats and a
// publish get each image embedded (inlineGuideImages). Bundles carry them too
// (guide_artifact.go).
package scribe

import (
	"encoding/base64"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// guideImagesTable holds a guide's pictures, keyed "<guide id>/<image id>".
const guideImagesTable = "guide_images"

// maxGuideImageBytes caps one picture: a full-screen screenshot fits with room
// to spare; a photo library does not belong in a guide.
const maxGuideImageBytes = 5 << 20

// guideImageTypes are the formats a picture may be, by sniffed content type.
// Raster only: SVG can carry script, and these four render everywhere.
var guideImageTypes = map[string]bool{
	"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true,
}

// guideImage is one stored picture.
type guideImage struct {
	Mime    string `json:"mime"`
	Data    []byte `json:"data"`
	Name    string `json:"name,omitempty"`
	Created string `json:"created"`
}

func guideImageKey(guideID, imageID string) string { return guideID + "/" + imageID }

// guideImagePath is how a guide's markdown points at a stored picture. Absolute,
// because the markdown is shown on more pages than Scribe's own.
func (T *Scribe) guideImagePath(guideID, imageID string) string {
	return T.WebPath() + "/img?g=" + url.QueryEscape(guideID) + "&i=" + url.QueryEscape(imageID)
}

// guideImageRefRE finds a stored-picture reference in markdown or HTML: the
// guide id and the image id, with the & raw or escaped.
var guideImageRefRE = regexp.MustCompile(`/img\?g=([A-Za-z0-9_-]+)&(?:amp;)?i=([A-Za-z0-9_-]+)`)

// handleImageUpload stores one picture for a guide: POST ?id=<guide> with a
// multipart "file". Answers {url, markdown}, the markdown ready to insert where
// the cursor was. Only someone who may edit the guide may add to it.
func (T *Scribe) handleImageUpload(w http.ResponseWriter, r *http.Request, udb Database, user string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	g, ownerUDB, _, canEdit, found := T.resolve(r, udb, user, id)
	if !found {
		http.NotFound(w, r)
		return
	}
	if !canEdit {
		http.Error(w, "you don't have edit access to this guide", http.StatusForbidden)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxGuideImageBytes+(1<<20))
	file, hdr, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "an image file is required (at most 5 MB)", http.StatusBadRequest)
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxGuideImageBytes+1))
	if err != nil {
		http.Error(w, "could not read the image", http.StatusBadRequest)
		return
	}
	if len(data) > maxGuideImageBytes {
		http.Error(w, "that image is larger than 5 MB", http.StatusRequestEntityTooLarge)
		return
	}
	// The browser's word for the type is not trusted: the bytes decide.
	mime := http.DetectContentType(data)
	if !guideImageTypes[mime] {
		http.Error(w, "use a PNG, JPEG, GIF or WebP image", http.StatusUnsupportedMediaType)
		return
	}
	name := ""
	if hdr != nil {
		name = strings.TrimSpace(hdr.Filename)
	}
	imgID := newID()
	ownerUDB.Set(guideImagesTable, guideImageKey(g.ID, imgID), guideImage{Mime: mime, Data: data, Name: name, Created: now()})
	path := T.guideImagePath(g.ID, imgID)
	md := "![" + imageAltFrom(name) + "](" + path + ")"
	// place=1: a picture dropped on the rendered guide, not into an editor.
	// It goes into the section it was dropped on (section=<id>), or the last
	// one, and the guide is saved: the editor's cursor is the other way in.
	if r.URL.Query().Get("place") != "" {
		title := placeImageInSection(&g, strings.TrimSpace(r.URL.Query().Get("section")), md)
		if title == "" {
			http.Error(w, "the guide has no section to put a picture in yet: add one first", http.StatusBadRequest)
			return
		}
		saveGuideRev(ownerUDB, g, "Added a picture to "+title)
		writeJSON(w, map[string]any{"url": path, "markdown": md, "placed": true, "section": title})
		return
	}
	writeJSON(w, map[string]string{"url": path, "markdown": md})
}

// placeImageInSection appends an image's markdown to the section with that
// id, or to the last section when the id names none, as its own paragraph.
// Returns the section's title, or "" when the guide has no sections.
func placeImageInSection(g *Guide, sectionID, md string) string {
	if len(g.Sections) == 0 {
		return ""
	}
	idx := -1
	for i, s := range g.Sections {
		if sectionID != "" && s.ID == sectionID {
			idx = i
		}
	}
	if idx < 0 {
		sorted := g.sorted()
		last := sorted[len(sorted)-1].ID
		for i, s := range g.Sections {
			if s.ID == last {
				idx = i
			}
		}
	}
	body := strings.TrimRight(g.Sections[idx].Markdown, "\n ")
	if body != "" {
		body += "\n\n"
	}
	g.Sections[idx].Markdown = body + md + "\n"
	return g.Sections[idx].Title
}

// imageAltFrom turns an uploaded file name into alt text: a pasted screenshot
// arrives as "image.png", which says nothing, so that becomes "Screenshot".
func imageAltFrom(name string) string {
	base := strings.TrimSpace(name)
	if i := strings.LastIndex(base, "."); i > 0 {
		base = base[:i]
	}
	base = strings.NewReplacer("[", "", "]", "", "_", " ", "-", " ").Replace(base)
	if base == "" || strings.EqualFold(base, "image") || strings.EqualFold(base, "pasted image") {
		return "Screenshot"
	}
	return base
}

// handleImageServe serves a stored picture to anyone who may read its guide:
// GET ?g=<guide>&i=<image>.
func (T *Scribe) handleImageServe(w http.ResponseWriter, r *http.Request, udb Database, user string) {
	gid := strings.TrimSpace(r.URL.Query().Get("g"))
	iid := strings.TrimSpace(r.URL.Query().Get("i"))
	g, ownerUDB, _, _, found := T.resolve(r, udb, user, gid)
	if !found || iid == "" {
		http.NotFound(w, r)
		return
	}
	var img guideImage
	if !ownerUDB.Get(guideImagesTable, guideImageKey(g.ID, iid), &img) || !guideImageTypes[img.Mime] {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", img.Mime)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=86400")
	_, _ = w.Write(img.Data)
}

// inlineGuideImages returns a copy of g whose pictures are embedded as data
// URLs, for what leaves gohort (an export, a publish) where /scribe/img means
// nothing. A reference to a picture that is not stored is left as it was.
func inlineGuideImages(g Guide, udb Database) Guide {
	if udb == nil {
		return g
	}
	out := g
	out.Sections = make([]Section, len(g.Sections))
	for i, s := range g.Sections {
		s.Markdown = inlineImageRefs(s.Markdown, udb)
		out.Sections[i] = s
	}
	// The header image too, when it is an uploaded picture.
	if ref := guideImageRefRE.FindStringSubmatch(g.ImageURL); ref != nil {
		var img guideImage
		if udb.Get(guideImagesTable, guideImageKey(ref[1], ref[2]), &img) && guideImageTypes[img.Mime] {
			out.ImageURL = "data:" + img.Mime + ";base64," + base64.StdEncoding.EncodeToString(img.Data)
		}
	}
	return out
}

// guideImageMarkdownRE matches a markdown image whose URL is a stored picture,
// so the whole URL (with any host in front) is replaced, not just its path.
var guideImageMarkdownRE = regexp.MustCompile(`(!\[[^\]\n]*\]\()([^\s)]*/img\?g=[A-Za-z0-9_-]+&(?:amp;)?i=[A-Za-z0-9_-]+)(\))`)

func inlineImageRefs(md string, udb Database) string {
	return guideImageMarkdownRE.ReplaceAllStringFunc(md, func(m string) string {
		parts := guideImageMarkdownRE.FindStringSubmatch(m)
		ref := guideImageRefRE.FindStringSubmatch(parts[2])
		if ref == nil {
			return m
		}
		var img guideImage
		if !udb.Get(guideImagesTable, guideImageKey(ref[1], ref[2]), &img) || !guideImageTypes[img.Mime] {
			return m
		}
		return parts[1] + "data:" + img.Mime + ";base64," + base64.StdEncoding.EncodeToString(img.Data) + parts[3]
	})
}

// deleteGuideImages removes every picture stored for a guide.
func deleteGuideImages(udb Database, guideID string) {
	prefix := guideID + "/"
	for _, k := range udb.Keys(guideImagesTable) {
		if strings.HasPrefix(k, prefix) {
			udb.Unset(guideImagesTable, k)
		}
	}
}

// dataImageURLRE matches an embedded raster on its own (a header image URL).
var dataImageURLRE = regexp.MustCompile(`^data:image/(?:png|jpeg|gif|webp);base64,([A-Za-z0-9+/=]+)$`)

// dataImageMarkdownRE matches a markdown image whose URL is an embedded raster.
var dataImageMarkdownRE = regexp.MustCompile(`(!\[[^\]\n]*\]\()data:image/(?:png|jpeg|gif|webp);base64,([A-Za-z0-9+/=]+)(\))`)

// storeEmbeddedImages moves pictures embedded in g's markdown into the store
// and points the markdown at them instead, so History copies a link rather
// than the picture on every edit. A bundle carries its pictures embedded, and
// so does a re-imported HTML export; this is how both land as ordinary stored
// pictures. g.ID must already be the guide's own. One that does not decode, is
// too large, or is not really the raster it claims is left as it was.
func (T *Scribe) storeEmbeddedImages(g *Guide, udb Database) {
	if udb == nil || g.ID == "" {
		return
	}
	// The header image the same way: a bundle carries an uploaded one embedded.
	if m := dataImageURLRE.FindStringSubmatch(g.ImageURL); m != nil {
		if data, err := base64.StdEncoding.DecodeString(m[1]); err == nil && len(data) > 0 && len(data) <= maxGuideImageBytes {
			if mime := http.DetectContentType(data); guideImageTypes[mime] {
				imgID := newID()
				udb.Set(guideImagesTable, guideImageKey(g.ID, imgID), guideImage{Mime: mime, Data: data, Created: now()})
				g.ImageURL = T.guideImagePath(g.ID, imgID)
			}
		}
	}
	for i := range g.Sections {
		g.Sections[i].Markdown = dataImageMarkdownRE.ReplaceAllStringFunc(g.Sections[i].Markdown, func(m string) string {
			parts := dataImageMarkdownRE.FindStringSubmatch(m)
			data, err := base64.StdEncoding.DecodeString(parts[2])
			if err != nil || len(data) == 0 || len(data) > maxGuideImageBytes {
				return m
			}
			mime := http.DetectContentType(data)
			if !guideImageTypes[mime] {
				return m
			}
			imgID := newID()
			udb.Set(guideImagesTable, guideImageKey(g.ID, imgID), guideImage{Mime: mime, Data: data, Created: now()})
			return parts[1] + T.guideImagePath(g.ID, imgID) + parts[3]
		})
	}
}
