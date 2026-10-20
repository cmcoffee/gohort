package orchestrate

// Agents hand each other pictures, audio and video, both ways.
//
// They passed text and nothing else. A song a music agent made reached the
// agent that asked for it as a sentence saying it was made, and on a channel
// turn it reached nobody: the wrapper claimed it for a page there was not.
// And an agent could not give the agent it ran a photo someone had sent it.
//
// Back: what the run attached goes onto the session of the agent that ran it
// (handBack), so it leaves with that one's reply, wherever the reply goes. On
// a web turn what a page already showed live stays where it is, so nothing is
// delivered twice. A delegation has no shared session, so its run hands its
// media to a sink on its context (withMediaReturn) that the caller empties.
//
// Out: the caller names what to hand over in `attach`, by its own names for
// it (handOut), and each item arrives as the run's own inbound media, media#1
// onward, exactly as a photo a person sent would (receiveHanded). Any other
// file in its folder is copied into the run's folder, under received/: agents
// outside one family work in folders of their own (workspace_scope.go), so a
// file named in the message was a file the other agent could not open.
//
// Files: what the run's tools created comes back too, copied into the folder
// of the agent that ran it when the two work in different folders
// (handBackFiles). A song a music agent made sat in that agent's folder while
// the agent that asked for it looked in its own and found nothing.

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// handBack moves what the agent a run dispatched attached (a picture, a song,
// a file) onto the session of the agent that ran it, so it goes out with that
// one's reply wherever the reply goes, and returns the line that tells it so.
// What a page already showed live stays where it is. Before, agents passed
// each other text and nothing else: a music agent's song reached its caller
// as a sentence saying it was made.
func handBack(agent string, from, to *ToolSession) string {
	made := handBackFiles(agent, madeFiles(from), to)
	images, videos, files := from.HandOverUnflushed(to)
	var got []string
	for _, c := range []struct {
		n          int
		one, other string
	}{{images, "an image", "%d images"}, {videos, "an audio or video file", "%d audio or video files"}, {files, "a file", "%d files"}} {
		switch {
		case c.n == 1:
			got = append(got, c.one)
		case c.n > 1:
			got = append(got, fmt.Sprintf(c.other, c.n))
		}
	}
	if len(got) == 0 {
		return made
	}
	return fmt.Sprintf("\n\n[%s attached %s. It is part of your turn now and goes out with your reply: do not attach it again, and do not say it is missing.]", agent, strings.Join(got, " and ")) + made
}

// handBackFileLimit caps what one hand-back copies, so a run that wrote a
// large download does not double it on disk unasked.
var handBackFileLimit int64 = 200 << 20

// madeFiles is what a run's tools created, as absolute paths. A session
// records the names relative to its folder; a collecting session (the sink a
// delegation hands its media to) is given absolute ones (returnMadeFiles), as
// it has no folder of its own.
func madeFiles(s *ToolSession) []string {
	var out []string
	for _, n := range s.StagedFiles() {
		if filepath.IsAbs(n) {
			out = append(out, n)
			continue
		}
		if s.WorkspaceDir == "" {
			continue
		}
		if p, err := ResolveWorkspacePath(s.WorkspaceDir, n); err == nil {
			out = append(out, p)
		}
	}
	return out
}

// returnMadeFiles records what run's tools created on sink, the session
// collecting what a delegation hands back, by absolute path.
func returnMadeFiles(run, sink *ToolSession) {
	if sink != nil && sink != run {
		sink.AddStagedFiles(madeFiles(run))
	}
}

// handBackFiles copies the files a run made into the folder of the session it
// is handing back to, and returns the line that tells that agent so. A file
// already in that folder (a sub-agent shares its parent's) is left alone, and
// so is one the run removed again (an attach with cleanup). A name the folder
// already holds is kept and the copy takes the next free name.
func handBackFiles(agent string, made []string, to *ToolSession) string {
	if to == nil || strings.TrimSpace(to.WorkspaceDir) == "" || len(made) == 0 {
		return ""
	}
	dir := to.WorkspaceDir
	var copied, tooBig []string
	var total int64
	for _, src := range made {
		if filepath.Dir(src) == filepath.Clean(dir) {
			continue
		}
		info, err := os.Stat(src)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if total+info.Size() > handBackFileLimit {
			tooBig = append(tooBig, filepath.Base(src))
			continue
		}
		name := freeFileName(dir, filepath.Base(src))
		if err := copyFileTo(src, filepath.Join(dir, name)); err != nil {
			Log("[agent_media] hand-back of %s from %s: %v", src, agent, err)
			continue
		}
		total += info.Size()
		copied = append(copied, name)
	}
	to.AddStagedFiles(copied)
	var b strings.Builder
	if len(copied) > 0 {
		fmt.Fprintf(&b, "\n\n[%s created %s. Copied into your working folder: attach any you mean to send.]",
			agent, strings.Join(copied, ", "))
	}
	if len(tooBig) > 0 {
		fmt.Fprintf(&b, "\n\n[%s also created %s, too large to copy to you; it stays in that agent's own folder.]",
			agent, strings.Join(tooBig, ", "))
	}
	return b.String()
}

// freeFileName is name, or name with the first free number before its
// extension when dir already holds one by that name.
func freeFileName(dir, name string) string {
	if _, err := os.Stat(filepath.Join(dir, name)); os.IsNotExist(err) {
		return name
	}
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 2; ; i++ {
		alt := fmt.Sprintf("%s-%d%s", stem, i, ext)
		if _, err := os.Stat(filepath.Join(dir, alt)); os.IsNotExist(err) {
			return alt
		}
	}
}

func copyFileTo(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	return out.Close()
}

type mediaReturnKey struct{}

// withMediaReturn is ctx for a run whose attachments go to sink when it ends:
// a delegation, whose result is otherwise text alone. The caller then hands
// them on (handBack).
func withMediaReturn(ctx context.Context, sink *ToolSession) context.Context {
	return context.WithValue(ctx, mediaReturnKey{}, sink)
}

// mediaReturnFrom is the session a run's attachments go back to, or nil.
func mediaReturnFrom(ctx context.Context) *ToolSession {
	if ctx == nil {
		return nil
	}
	s, _ := ctx.Value(mediaReturnKey{}).(*ToolSession)
	return s
}

// handedItem is one attachment an agent hands the agent it runs.
type handedItem struct {
	kind string // "image", "video" (audio rides video, as everywhere) or "file"
	raw  []byte // the media; empty for a file
	path string // a file's absolute path in the caller's folder; empty for media
	was  string // what the caller called it
	from string // the agent that handed it over
}

// handedFilesDir is where a handed file lands in the receiving agent's folder.
// A folder of its own, so the note that names a file can say exactly where it
// is before the receiving folder is known, and a handed file never lands on
// one of the receiver's own.
const handedFilesDir = "received"

type handedMediaKey struct{}

// withHandedMedia is ctx for a run that receives items as its inbound media,
// for the paths that build the run's session out of the caller's sight (a
// delegation, a handed-off run).
func withHandedMedia(ctx context.Context, items []handedItem) context.Context {
	if len(items) == 0 {
		return ctx
	}
	return context.WithValue(ctx, handedMediaKey{}, items)
}

// handedMediaFrom is what ctx hands the run it starts, or nil.
func handedMediaFrom(ctx context.Context) []handedItem {
	if ctx == nil {
		return nil
	}
	items, _ := ctx.Value(handedMediaKey{}).([]handedItem)
	return items
}

// handOut resolves the attachments an agent names for the agent it runs,
// against its own session: something sent to it (media#N), an image it kept,
// a picture, audio or video file, or any other file in its working folder. It
// returns them with the line that tells the receiving agent what it was
// handed. A name that resolves to nothing is refused, so the caller hears it
// rather than the run going out without it, and so is a set of files too
// large to copy.
func handOut(caller *ToolSession, from string, refs []string) ([]handedItem, string, error) {
	var items []handedItem
	var missing, lines []string
	media := 0
	var fileBytes int64
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		// A file in the caller's folder that is not a picture, audio or video
		// goes over as a file. Checked first: the media resolver takes any
		// file it is pointed at, and handed a text file as a broken picture.
		if path, size, isFile := handableFile(caller, ref); isFile && fileMediaKind(path) == "" {
			if fileBytes += size; fileBytes > handBackFileLimit {
				return nil, "", fmt.Errorf("nothing was sent: the files to hand over come to more than %d MB, too large to copy. Hand over fewer, or put what the agent needs in the message", handBackFileLimit>>20)
			}
			items = append(items, handedItem{kind: "file", path: path, was: ref, from: from})
			lines = append(lines, fmt.Sprintf("%s/%s (a file, which %s called %s, now in your working folder)", handedFilesDir, filepath.Base(path), from, ref))
			continue
		}
		b64, kind, ok := resolveAttachmentRef(caller, ref, true)
		raw, err := base64.StdEncoding.DecodeString(b64)
		if !ok || err != nil || len(raw) == 0 {
			missing = append(missing, ref)
			continue
		}
		if kind != "video" {
			kind = "image"
		}
		items = append(items, handedItem{kind: kind, raw: raw, was: ref, from: from})
		media++
		what := "an image"
		if kind == "video" {
			what = "an audio or video file"
		}
		lines = append(lines, fmt.Sprintf("media#%d (%s, which %s called %s)", media, what, from, ref))
	}
	if len(missing) > 0 {
		return nil, "", fmt.Errorf("nothing was sent: %s is not something you can hand over. attach takes media#N (something sent to you), an image you kept, or a file in your working folder", strings.Join(missing, ", "))
	}
	if len(items) == 0 {
		return nil, "", nil
	}
	return items, fmt.Sprintf("\n\n[%s handed you, with this request: %s. Use media by its id, as you would something a person sent you, and a file by its path.]", from, strings.Join(lines, "; ")), nil
}

// fileMediaKind is "image" or "video" (audio rides video) for a picture, sound
// or video file, by its extension or, without a known one, its first bytes;
// "" for anything else.
func fileMediaKind(path string) string {
	t := mime.TypeByExtension(strings.ToLower(filepath.Ext(path)))
	if t == "" {
		if f, err := os.Open(path); err == nil {
			head := make([]byte, 512)
			n, _ := f.Read(head)
			f.Close()
			t = http.DetectContentType(head[:n])
		}
	}
	switch {
	case strings.HasPrefix(t, "image/"):
		return "image"
	case strings.HasPrefix(t, "video/"), strings.HasPrefix(t, "audio/"):
		return "video"
	}
	return ""
}

// handableFile is ref as a file in the caller's own folder: its absolute path
// and size. A folder, a path outside it, or nothing there is not one.
func handableFile(caller *ToolSession, ref string) (string, int64, bool) {
	if caller == nil || strings.TrimSpace(caller.WorkspaceDir) == "" {
		return "", 0, false
	}
	path, err := ResolveWorkspacePath(caller.WorkspaceDir, ref)
	if err != nil {
		return "", 0, false
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", 0, false
	}
	return path, info.Size(), true
}

// receiveHanded makes what an agent was handed its own inbound media, in the
// order handOut numbered them.
//
// A file is copied into the run's folder under received/, by its own name: a
// copy from the same sender replaces the one it sent before. Copied even when
// the two share a folder (a sub-agent and its parent), so the path the note
// gave is right either way.
func receiveHanded(sess *ToolSession, items []handedItem) {
	for _, it := range items {
		if it.kind != "file" {
			sess.RegisterInboundMedia(it.kind, it.raw, it.from)
			continue
		}
		if err := receiveHandedFile(sess, it.path); err != nil {
			Log("[agent_media] %s handed %s: not copied: %v", it.from, it.was, err)
		}
	}
}

func receiveHandedFile(sess *ToolSession, src string) error {
	if sess == nil || strings.TrimSpace(sess.WorkspaceDir) == "" {
		return fmt.Errorf("the run has no working folder")
	}
	dir := filepath.Join(sess.WorkspaceDir, handedFilesDir)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	dst := filepath.Join(dir, filepath.Base(src))
	if dst == src {
		return nil
	}
	os.Remove(dst)
	return copyFileTo(src, dst)
}

// takeHandedMedia gives sess what ctx hands the run, and returns ctx without
// it: handed to this run only, not to one it starts in turn.
func takeHandedMedia(ctx context.Context, sess *ToolSession) context.Context {
	items := handedMediaFrom(ctx)
	if len(items) == 0 {
		return ctx
	}
	receiveHanded(sess, items)
	return context.WithValue(ctx, handedMediaKey{}, []handedItem(nil))
}

// handerName is the agent that delegates, by name, for the line that tells
// the receiving agent who handed it something.
func handerName(owner, agentID string) string {
	if rec, ok := findAgentByNameOrID(UserDB(orchestrateBaseDB, owner), owner, agentID); ok && strings.TrimSpace(rec.Name) != "" {
		return rec.Name
	}
	return chFirst(strings.TrimSpace(agentID), "the agent that asked")
}
