package server

import (
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"quasar/internal/db"
	"quasar/internal/docker"
	"quasar/internal/files"
)

// maxUploadRequest bounds one upload request as a whole, so a client cannot
// stream forever by sending many parts. files.MaxUpload bounds each file inside
// it; this bounds the request that carries them.
const maxUploadRequest = 4 * files.MaxUpload

// The storage explorer. Two things can be browsed — the mounts of one
// application, and any Docker volume on the server — and both come down to the
// same question: given a path on the host, where can this process read it?
//
// Every route here is admin-only. What lives in these trees is an application's
// data: database files, uploads, the configuration an image wrote for itself,
// and the credentials some of them keep in plain text. That is the same
// material a backup archive holds, and the backup download is admin-only for
// the same reason.

// browseRoot maps a path on the host to a root this process can read, and
// sometimes write.
//
// The dashboard can see the same file up to three ways, and which one applies
// is what decides whether it is editable.
//
//   - /opt/quasar/apps is bind-mounted into this container at its own host
//     path, read-write. An app's data directory is therefore reachable directly
//     and can be changed, which covers everything a catalogue app keeps.
//   - Docker's volume tree is not mounted by default. When an operator has
//     mounted it — see the commented-out line in docker-compose.yml — the
//     volume's own mountpoint exists inside this container and is used as it
//     is. This is only ever tried for a Docker volume, whose mountpoint lives
//     under the daemon's own directory: trying it for an arbitrary bind mount
//     would resolve /etc against the container's /etc rather than the host's.
//   - Everything else goes through HOST_ROOT, the read-only mount of the whole
//     host filesystem. Readable, never writable.
//
// Which of those happened is not recorded anywhere: files.NewRoot asks the
// kernel whether it can write, so the answer is the filesystem's rather than
// this function's opinion of it.
func (s *Server) browseRoot(hostPath string, dockerVolume bool) (files.Root, error) {
	if hostPath == "" {
		return files.Root{}, files.ErrNotDir
	}
	local := filepath.Clean(hostPath)
	switch {
	case underAppsDir(s.cfg.AppsDir, local):
	case dockerVolume && isDir(local):
	default:
		local = filepath.Join(s.cfg.HostRootPath, local)
	}
	return files.NewRoot(local)
}

func isDir(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

// underAppsDir reports whether a host path is inside the apps directory, which
// is the one tree mounted at its own path.
func underAppsDir(appsDir, p string) bool {
	appsDir = filepath.Clean(appsDir)
	return p == appsDir || strings.HasPrefix(p, appsDir+string(filepath.Separator))
}

// browseTarget is one browsable place, resolved from the URL: where to read,
// and everything the page needs to say where the reader is.
type browseTarget struct {
	Kind      string // "app" or "volume"
	Ref       string // the app ID or the volume name
	Mount     string // which of an app's mounts, by container path
	Title     string
	Subtitle  string // what the root is, in the operator's terms
	HostPath  string
	BackURL   string
	BackLabel string
	Root      files.Root

	// App is set for kind "app", and carries the mounts the picker offers when
	// the URL names no single one.
	App    *db.App
	Mounts []docker.Mount
	Volume docker.Volume
}

// MountURL is the explorer URL for one of an app's mounts, which is what the
// picker links to when the app has more than one.
func (t browseTarget) MountURL(dest string) string {
	return "/files/" + t.Kind + "/" + url.PathEscape(t.Ref) + "?mount=" + url.QueryEscape(dest)
}

// Writable reports whether this storage takes changes, which is what decides
// whether the interface offers any.
func (t browseTarget) Writable() bool { return t.Root.Writable() }

// UploadURL, SaveURL and DeleteURL are where the three write actions post.
//
// Save and delete take the file they act on from their form, because it is
// whichever one the dialog has open rather than anything the page's URL knows.
// Upload cannot: its body is a multipart stream read part by part, and asking
// for a form value would consume it, so the directory being filled travels in
// the query instead.
func (t browseTarget) UploadURL(dir string) string {
	q := url.Values{}
	if clean := files.Clean(dir); clean != "" {
		q.Set("path", clean)
	}
	return t.actionURL("upload", q)
}

func (t browseTarget) SaveURL() string   { return t.actionURL("save", nil) }
func (t browseTarget) DeleteURL() string { return t.actionURL("delete", nil) }

func (t browseTarget) actionURL(verb string, q url.Values) string {
	if q == nil {
		q = url.Values{}
	}
	if t.Mount != "" {
		q.Set("mount", t.Mount)
	}
	u := "/files/" + t.Kind + "/" + url.PathEscape(t.Ref) + "/" + verb
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return u
}

// URLFor is the explorer URL for one path inside this target. Templates build
// every link and every hx-get through it, so the query keeps carrying the mount
// as the reader walks down the tree.
func (t browseTarget) URLFor(rel string) string {
	return t.linkFor("/files/", rel)
}

// PartialFor is the same path as an htmx fetch of the listing alone.
func (t browseTarget) PartialFor(rel string) string {
	return t.linkFor("/partials/files/", rel)
}

// PreviewFor is the URL of one file's preview panel, and EditFor the same panel
// with the text in a box that takes typing.
func (t browseTarget) PreviewFor(rel string) string {
	return t.linkFor("/partials/files/", rel, "preview", "1")
}

func (t browseTarget) EditFor(rel string) string {
	return t.linkFor("/partials/files/", rel, "preview", "1", "edit", "1")
}

// RawFor is the URL that downloads one file.
func (t browseTarget) RawFor(rel string) string {
	return t.linkFor("/files/", rel, "raw", "1")
}

// InlineFor is the URL that serves one file for a browser to render in place.
// Only the image types serveFile will actually send inline are ever asked for
// through it.
func (t browseTarget) InlineFor(rel string) string {
	return t.linkFor("/files/", rel, "raw", "1", "inline", "1")
}

func (t browseTarget) linkFor(prefix, rel string, extra ...string) string {
	q := url.Values{}
	if t.Mount != "" {
		q.Set("mount", t.Mount)
	}
	if clean := files.Clean(rel); clean != "" {
		q.Set("path", clean)
	}
	for i := 0; i+1 < len(extra); i += 2 {
		q.Set(extra[i], extra[i+1])
	}
	u := prefix + t.Kind + "/" + url.PathEscape(t.Ref)
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return u
}

// resolveTarget works out what the URL points at, writing the error response
// itself and returning ok=false when it cannot.
func (s *Server) resolveTarget(w http.ResponseWriter, r *http.Request) (browseTarget, bool) {
	switch r.PathValue("kind") {
	case "app":
		return s.resolveAppTarget(w, r)
	case "volume":
		return s.resolveVolumeTarget(w, r)
	}
	http.Error(w, "unknown storage source", http.StatusNotFound)
	return browseTarget{}, false
}

func (s *Server) resolveAppTarget(w http.ResponseWriter, r *http.Request) (browseTarget, bool) {
	a, err := db.GetApp(s.db, s.keyring, r.PathValue("ref"))
	if err != nil {
		http.Error(w, "application not found", http.StatusNotFound)
		return browseTarget{}, false
	}
	mounts := s.dock.AppMounts(r.Context(), a)
	t := browseTarget{
		Kind:      "app",
		Ref:       a.ID,
		Title:     a.Name,
		BackURL:   "/apps/" + a.ID,
		BackLabel: a.Name,
		App:       a,
		Mounts:    mounts,
	}

	want := r.URL.Query().Get("mount")
	// One mount needs no choosing, and asking the reader to pick from a list of
	// one would be a click that says nothing.
	if want == "" && len(mounts) == 1 {
		want = mounts[0].Destination
	}
	if want == "" {
		// The picker. Not an error the reader caused, so the page renders it
		// rather than a message.
		t.Subtitle = "Storage"
		return t, true
	}

	m, ok := mountByDestination(mounts, want)
	if !ok {
		// Resolving against the app's own mounts is what stops a URL naming a
		// path this application never mounted from opening it.
		http.Error(w, "this application has no such mount", http.StatusNotFound)
		return browseTarget{}, false
	}
	t.Mount = m.Destination
	t.Subtitle = m.Destination
	t.HostPath = m.Source
	if m.IsVolume() {
		t.Subtitle = m.Destination + " — volume " + m.Name
	}

	root, err := s.browseRoot(m.Source, m.IsVolume())
	if err != nil {
		s.browseUnavailable(w, r, t, err)
		return browseTarget{}, false
	}
	// A mount the container itself holds read-only is one the application was
	// deliberately not given write access to. The dashboard writing to it
	// anyway would be going behind the compose file that said so.
	if !m.RW {
		root = root.ReadOnly()
	}
	t.Root = root
	return t, true
}

func (s *Server) resolveVolumeTarget(w http.ResponseWriter, r *http.Request) (browseTarget, bool) {
	name := r.PathValue("ref")
	v, err := s.dock.Volume(r.Context(), name)
	if err != nil {
		http.Error(w, "volume not found", http.StatusNotFound)
		return browseTarget{}, false
	}
	t := browseTarget{
		Kind:      "volume",
		Ref:       v.Name,
		Title:     v.Name,
		Subtitle:  "Docker volume",
		HostPath:  v.Mountpoint,
		BackURL:   "/system",
		BackLabel: "System",
		Volume:    v,
	}
	if !v.Local() {
		s.browseUnavailable(w, r, t, fmt.Errorf(
			"this volume is on the %q driver, so its data is not on this machine's filesystem", v.Driver))
		return browseTarget{}, false
	}
	root, err := s.browseRoot(v.Mountpoint, true)
	if err != nil {
		s.browseUnavailable(w, r, t, err)
		return browseTarget{}, false
	}
	t.Root = root
	return t, true
}

func mountByDestination(mounts []docker.Mount, dest string) (docker.Mount, bool) {
	for _, m := range mounts {
		if m.Destination == dest {
			return m, true
		}
	}
	return docker.Mount{}, false
}

// browseUnavailable explains a root that cannot be opened, which is a normal
// thing to meet rather than a fault: a volume on a network driver, a bind mount
// to a path that does not exist on this host, a mount whose source has been
// deleted under the daemon.
func (s *Server) browseUnavailable(w http.ResponseWriter, r *http.Request, t browseTarget, cause error) {
	reason := "This storage cannot be opened from here."
	switch {
	case errors.Is(cause, files.ErrNotDir):
		reason = "This mount is a single file, not a directory — there is nothing to list."
	case errors.Is(cause, os.ErrNotExist):
		reason = "Nothing exists at this path on the host any more."
	case errors.Is(cause, os.ErrPermission):
		reason = "The dashboard is not allowed to read this path."
	default:
		if cause != nil {
			reason = strings.ToUpper(cause.Error()[:1]) + cause.Error()[1:] + "."
		}
	}
	w.WriteHeader(http.StatusOK)
	s.render(w, r, "files", map[string]any{
		"Title":  t.Title,
		"Target": t,
		"Reason": reason,
	})
}

// handleFiles renders the explorer page, or serves a file's bytes when the URL
// asks for them.
//
// The download shares this route rather than having its own because it shares
// everything that matters: the same target resolution, the same cage, the same
// admin gate. A separate route would be a second place for all three to be got
// right.
func (s *Server) handleFiles(w http.ResponseWriter, r *http.Request) {
	t, ok := s.resolveTarget(w, r)
	if !ok {
		return
	}
	if r.URL.Query().Get("raw") == "1" {
		s.serveFile(w, r, t)
		return
	}
	rel := files.Clean(r.URL.Query().Get("path"))
	data := map[string]any{
		"Title":  t.Title,
		"Target": t,
		"Path":   rel,
		// An app with more than one mount and a URL that names none: the page
		// shows what it has to choose between rather than picking for the
		// reader.
		"Picker": t.Kind == "app" && t.Mount == "",
	}
	if t.Root.Valid() {
		// A URL naming a file rather than a directory is a link somebody kept —
		// the page opens its folder and puts the file itself up, which is what
		// the link was saved for.
		if info, err := t.Root.Stat(rel); err == nil && !info.IsDir() {
			data["Open"] = rel
			rel = files.Parent(rel)
			data["Path"] = rel
		}
		// The listing is rendered with the page rather than fetched after it.
		// Reading one directory is a syscall, not the seconds the System page's
		// sections cost, and a browser that flashed empty on every step down a
		// tree would feel slower than it is.
		listing, err := s.listing(t, rel)
		if err != nil {
			data["Error"] = listingError(err)
		} else {
			data["Listing"] = listing
		}
	}
	s.render(w, r, "files", data)
}

// handleFilesPartial answers the htmx fetches the page makes as the reader
// walks the tree: a directory listing, or one file's preview panel.
func (s *Server) handleFilesPartial(w http.ResponseWriter, r *http.Request) {
	t, ok := s.resolveTarget(w, r)
	if !ok {
		return
	}
	if !t.Root.Valid() {
		http.Error(w, "this storage cannot be opened", http.StatusNotFound)
		return
	}
	rel := files.Clean(r.URL.Query().Get("path"))

	if r.URL.Query().Get("preview") == "1" {
		preview, err := t.Root.Read(rel)
		if err != nil {
			s.renderPartial(w, "file_preview", map[string]any{
				"Target": t, "Path": rel, "Error": listingError(err),
			})
			return
		}
		editable := t.Root.Editable(preview)
		s.renderPartial(w, "file_preview", map[string]any{
			"Target":   t,
			"Path":     rel,
			"Name":     path.Base(rel),
			"Preview":  preview,
			"Editable": editable,
			// The same panel, with the text in a box that takes typing. Asked
			// for by the Edit button and never offered for a file the editor
			// could not hold whole.
			"Editing": editable && r.URL.Query().Get("edit") == "1",
		})
		return
	}

	listing, err := s.listing(t, rel)
	if err != nil {
		s.renderPartial(w, "file_list", map[string]any{
			"Target": t, "Path": rel, "Error": listingError(err),
		})
		return
	}
	s.renderPartial(w, "file_list", listing)
}

// listing reads one directory into what the file_list partial renders. The page
// and the htmx swap both go through it, so a directory reached by loading a URL
// and the same one reached by clicking into it cannot render differently.
func (s *Server) listing(t browseTarget, rel string) (map[string]any, error) {
	// Cleaned here as well as at the door, because Path is what the listing
	// builds its child links from: left as it arrived, a folder reached through
	// "a/../b" would hand every row below it a path that still carried the
	// detour.
	rel = files.Clean(rel)
	entries, err := t.Root.List(rel)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"Target":  t,
		"Path":    rel,
		"Crumbs":  files.Crumbs(rel),
		"Parent":  files.Parent(rel),
		"AtRoot":  rel == "",
		"Entries": entries,
	}, nil
}

// listingError turns a filesystem failure into something an operator can act
// on. The distinction that matters is between a path that is gone and a path
// the browser refused, and neither should arrive as a Go error string.
func listingError(err error) string {
	switch {
	case errors.Is(err, files.ErrOutsideRoot):
		return "That path leads outside this volume, so it is not shown."
	case errors.Is(err, files.ErrNotDir):
		return "That is not a directory."
	case errors.Is(err, os.ErrNotExist):
		return "That path no longer exists — it may have been removed since the page was loaded."
	case errors.Is(err, os.ErrPermission):
		return "The dashboard is not allowed to read that path."
	}
	return "That path could not be read."
}

// serveFile hands out one file's bytes.
//
// Everything goes out as an attachment except the handful of image types a
// browser cannot be made to execute. These files are written by application
// containers, and one served inline from the dashboard's own origin is a
// document in the admin session's origin: an HTML file left in a web root, or
// an SVG, would run its script against this dashboard. nosniff is what stops
// the browser from arriving at that conclusion on its own for the rest.
func (s *Server) serveFile(w http.ResponseWriter, r *http.Request, t browseTarget) {
	if !t.Root.Valid() {
		http.Error(w, "this storage cannot be opened", http.StatusNotFound)
		return
	}
	rel := files.Clean(r.URL.Query().Get("path"))
	f, info, err := t.Root.Open(rel)
	if err != nil {
		http.Error(w, listingError(err), http.StatusNotFound)
		return
	}
	defer f.Close()

	name := path.Base(rel)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if kind := files.ImageType(name); kind != "" && r.URL.Query().Get("inline") == "1" {
		w.Header().Set("Content-Type", kind)
		w.Header().Set("Content-Disposition", "inline; filename="+strconv.Quote(name))
	} else {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", "attachment; filename="+strconv.Quote(name))
	}
	http.ServeContent(w, r, name, info.ModTime(), f)
}

// The three writes. All of them answer with the listing the reader is looking
// at, so the table shows the result of what just happened rather than the state
// before it, and all of them record an audit entry: these change an
// application's data from outside the application, which is exactly the kind of
// thing somebody will later need to find in a trail.

// handleFilesUpload takes files into the directory being browsed.
func (s *Server) handleFilesUpload(w http.ResponseWriter, r *http.Request) {
	t, ok := s.resolveWritable(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadRequest)

	// Streamed rather than buffered: ParseMultipartForm would spool the whole
	// upload to a temporary file first, which for a few hundred megabytes means
	// writing it to the disk twice. Reading the parts in order is also why the
	// directory is read from the query — r.FormValue would consume the body
	// this is about to walk.
	dir := files.Clean(r.URL.Query().Get("path"))
	reader, err := r.MultipartReader()
	if err != nil {
		s.listingAfter(w, t, dir, flashErr("That upload could not be read."))
		return
	}
	saved, failed := storeUploads(t.Root, dir, reader)
	for _, name := range saved {
		s.audit(r, "storage.upload", t.auditTarget(), path.Join(dir, name))
	}

	switch {
	case len(saved) == 0 && len(failed) == 0:
		s.listingAfter(w, t, dir, flashErr("No file was chosen."))
	case len(failed) > 0:
		s.listingAfter(w, t, dir, flashErr(fmt.Sprintf(
			"%d uploaded, %d refused (%s). A file over %s, or a name that cannot be used, is refused.",
			len(saved), len(failed), strings.Join(failed, ", "), docker.HumanSize(files.MaxUpload))))
	default:
		s.listingAfter(w, t, dir, flashOK(fmt.Sprintf("Uploaded %s.", strings.Join(saved, ", "))))
	}
}

// storeUploads writes every file part of a multipart body into dir, and reports
// what landed and what did not.
//
// Parts are taken one at a time and streamed straight to disk, so an upload
// costs its own size in disk and almost nothing in memory. One part failing
// does not abandon the rest: a batch where a single file was too large should
// deliver the others and say which one it left.
func storeUploads(root files.Root, dir string, reader *multipart.Reader) (saved, failed []string) {
	for {
		part, err := reader.NextPart()
		if err != nil {
			return saved, failed
		}
		if part.FormName() != "files" || part.FileName() == "" {
			_ = part.Close() // this part is done with either way
			continue
		}
		// The filename is client-controlled, and the part is free to call
		// itself "../../etc/cron.d/backdoor". SafeName reduces it to one
		// component or refuses it; Save would refuse it a second time.
		name, ok := files.SafeName(part.FileName())
		if !ok {
			failed = append(failed, part.FileName())
			_ = part.Close() // this part is done with either way
			continue
		}
		_, err = root.Save(path.Join(dir, name), part, files.MaxUpload)
		_ = part.Close() // this part is done with either way
		if err != nil {
			failed = append(failed, name)
			continue
		}
		saved = append(saved, name)
	}
}

// errEditTooBig is the one refusal the editor has of its own: the file has
// outgrown what it can hold whole.
var errEditTooBig = errors.New("this file is larger than the editor can hold")

// writeEdit applies the editor's contents to one file.
func writeEdit(root files.Root, rel, content string) error {
	// The editor was filled from a preview, and a preview stops at 256 KB. If
	// the file on disk is larger than that — because it grew since the panel
	// was drawn, or because this request was made by hand — writing the
	// editor's copy over it would drop everything past the cut. The size is
	// taken from the file rather than trusted from the form, because the form
	// is the thing that would be lying.
	if info, err := root.Stat(rel); err == nil && info.Size() > files.MaxEdit {
		return errEditTooBig
	}
	// Browsers submit CRLF from a textarea whatever went into it. Writing that
	// into a config file read by a shell script on Linux produces errors that
	// are invisible in the editor that caused them.
	body := strings.ReplaceAll(content, "\r\n", "\n")
	_, err := root.Save(rel, strings.NewReader(body), files.MaxEdit)
	return err
}

// handleFilesSave writes the editor's contents back to one file.
func (s *Server) handleFilesSave(w http.ResponseWriter, r *http.Request) {
	t, ok := s.resolveWritable(w, r)
	if !ok {
		return
	}
	rel := files.Clean(r.FormValue("path"))
	if err := writeEdit(t.Root, rel, r.FormValue("content")); err != nil {
		s.previewAfter(w, t, rel, writeError(err))
		return
	}
	s.audit(r, "storage.edit", t.auditTarget(), rel)
	w.Header().Set("HX-Trigger", "quasar:files-changed")
	s.previewAfter(w, t, rel, "")
}

// handleFilesDelete removes one file.
func (s *Server) handleFilesDelete(w http.ResponseWriter, r *http.Request) {
	t, ok := s.resolveWritable(w, r)
	if !ok {
		return
	}
	rel := files.Clean(r.FormValue("path"))
	if err := t.Root.Remove(rel); err != nil {
		s.previewAfter(w, t, rel, writeError(err))
		return
	}
	s.audit(r, "storage.delete", t.auditTarget(), rel)
	// The file the dialog was showing is gone, so the dialog goes with it and
	// the listing behind it is asked to catch up.
	w.Header().Set("HX-Trigger", "quasar:files-changed, quasar:close-modal")
	w.WriteHeader(http.StatusOK)
}

// resolveWritable resolves the target and refuses anything that cannot be
// written, so no write handler has to remember to check.
//
// The check is not only belt and braces for a hidden button: read-only is a
// property of the mount, and a mount can be read-only now that was writable
// when the page was drawn.
func (s *Server) resolveWritable(w http.ResponseWriter, r *http.Request) (browseTarget, bool) {
	t, ok := s.resolveTarget(w, r)
	if !ok {
		return browseTarget{}, false
	}
	if !t.Root.Valid() {
		http.Error(w, "this storage cannot be opened", http.StatusNotFound)
		return browseTarget{}, false
	}
	if !t.Root.Writable() {
		http.Error(w, "This storage is read-only.", http.StatusForbidden)
		return browseTarget{}, false
	}
	return t, true
}

// auditTarget names what was changed, in the terms the audit page lists things
// under: the application, or the volume.
func (t browseTarget) auditTarget() string {
	if t.Kind == "app" {
		return t.Title
	}
	return "volume " + t.Ref
}

// listingAfter re-renders the directory a write happened in, carrying a note
// about what happened.
func (s *Server) listingAfter(w http.ResponseWriter, t browseTarget, dir string, flash map[string]string) {
	listing, err := s.listing(t, dir)
	if err != nil {
		s.renderPartial(w, "file_list", map[string]any{
			"Target": t, "Path": dir, "Error": listingError(err),
		})
		return
	}
	listing["Flash"] = flash
	s.renderPartial(w, "file_list", listing)
}

// previewAfter re-renders the file the dialog is showing, with a problem to
// report if there is one.
func (s *Server) previewAfter(w http.ResponseWriter, t browseTarget, rel, problem string) {
	preview, err := t.Root.Read(rel)
	if err != nil {
		s.renderPartial(w, "file_preview", map[string]any{
			"Target": t, "Path": rel, "Name": path.Base(rel), "Error": listingError(err),
		})
		return
	}
	s.renderPartial(w, "file_preview", map[string]any{
		"Target":   t,
		"Path":     rel,
		"Name":     path.Base(rel),
		"Preview":  preview,
		"Editable": t.Root.Editable(preview),
		"Problem":  problem,
	})
}

func flashOK(text string) map[string]string  { return map[string]string{"Kind": "ok", "Text": text} }
func flashErr(text string) map[string]string { return map[string]string{"Kind": "err", "Text": text} }

// writeError turns a refused write into a sentence about what was refused.
func writeError(err error) string {
	switch {
	case errors.Is(err, errEditTooBig):
		return "This file has grown past what the editor can hold, so nothing was written — saving would have dropped the rest of it."
	case errors.Is(err, files.ErrReadOnly):
		return "This storage is read-only."
	case errors.Is(err, files.ErrTooLarge):
		return "That is larger than this can write."
	case errors.Is(err, files.ErrIsDir):
		return "That is a directory. Only files can be changed here."
	case errors.Is(err, files.ErrNotRegular):
		return "That is not a plain file — a link or a device — so it was left alone."
	case errors.Is(err, files.ErrBadName), errors.Is(err, files.ErrOutsideRoot):
		return "That name cannot be used here."
	case errors.Is(err, os.ErrNotExist):
		return "That path no longer exists."
	case errors.Is(err, os.ErrPermission):
		return "The dashboard is not allowed to write there."
	}
	return "That change could not be written."
}

// handleAppStoragePartial fills the Storage section of an application's page:
// what it has mounted, and a way into each one.
func (s *Server) handleAppStoragePartial(w http.ResponseWriter, r *http.Request) {
	a := s.getApp(w, r)
	if a == nil {
		return
	}
	mounts := s.dock.AppMounts(r.Context(), a)
	rows := make([]map[string]any, 0, len(mounts))
	for _, m := range mounts {
		rows = append(rows, map[string]any{
			"Mount": m,
			"URL":   "/files/app/" + a.ID + "?mount=" + url.QueryEscape(m.Destination),
			// Whether the path is readable is worth knowing before the click, and
			// costs one stat.
			"Readable": s.readable(m.Source, m.IsVolume()),
		})
	}
	s.renderPartial(w, "app_storage", map[string]any{
		"App":    a,
		"Mounts": rows,
	})
}

// handleSystemVolumesPartial fills the Volumes section of the System page: every
// volume on the server, what holds it, and a way in.
func (s *Server) handleSystemVolumesPartial(w http.ResponseWriter, r *http.Request) {
	vols, err := s.dock.Volumes(r.Context())
	if err != nil {
		s.renderPartial(w, "system_volumes", map[string]any{"Error": true})
		return
	}
	// Names for the apps the volumes belong to, so a row reads as the app it is
	// part of rather than as a compose project nobody named.
	names := map[string]string{}
	if apps, err := db.ListApps(s.db, s.keyring); err == nil {
		for _, a := range apps {
			names[a.ID] = a.Name
		}
	}
	rows := make([]map[string]any, 0, len(vols))
	var total int64
	for _, v := range vols {
		total += v.Bytes
		rows = append(rows, map[string]any{
			"Volume":  v,
			"AppName": names[v.AppID],
			"URL":     "/files/volume/" + url.PathEscape(v.Name),
		})
	}
	s.renderPartial(w, "system_volumes", map[string]any{
		"Volumes": rows,
		"Total":   total,
	})
}

// readable reports whether a host path can be opened as a directory from here.
func (s *Server) readable(hostPath string, dockerVolume bool) bool {
	_, err := s.browseRoot(hostPath, dockerVolume)
	return err == nil
}
