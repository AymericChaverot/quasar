package docker

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/build"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/volume"
)

// Reclaimable is one category of Docker state a sweep would remove.
type Reclaimable struct {
	Key   string // stable identifier; the page picks the row's icon from it
	Label string
	Count int
	Bytes int64
	Note  string // a few of the actual names, so the count is not the only thing shown

	// Objects is the category opened up: one entry per thing that would go, so
	// a count can be checked instead of trusted, and so a sweep can be narrowed
	// to some of them. Capped at objectsListed.
	Objects []ReclaimableObject

	// WholeOnly marks a category the daemon offers no per-object removal for.
	// The build cache is the only one: it has a prune endpoint and nothing
	// else, so its objects are listed to be read rather than ticked.
	WholeOnly bool
}

// ReclaimableObject is one thing inside a category — a line to read before
// deciding, and to tick if only some of them should go.
type ReclaimableObject struct {
	// ID is what a narrowed sweep names it by: an image or container ID, a
	// network or volume name. Never shown; Name is.
	ID    string
	Name  string
	Note  string // what it is, or how old — enough to recognise it by
	Bytes int64
}

// objectsListed caps how many objects a category hands to the page. A server
// that has been building for months can hold thousands of untagged layers, and
// a list that long is neither readable nor worth the bytes — the count above it
// is already the honest figure, and a sweep of the whole category still takes
// every one of them.
const objectsListed = 100

// CleanupScan is the preview of a sweep: what would go and what it is worth,
// worked out without removing anything.
//
// Showing this before the button is pressed is most of the point. "Prune
// dangling images" is a promise the operator cannot check, and on a server that
// has been deploying for months the honest answer — several gigabytes — is the
// only thing that makes the button worth pressing.
type CleanupScan struct {
	Items   []Reclaimable // categories safe to remove, in display order
	Volumes Reclaimable   // orphaned volumes: data loss, so opted into separately
	Count   int           // objects across Items
	Bytes   int64         // space across Items
}

// Empty reports whether a sweep would do nothing at all.
func (s CleanupScan) Empty() bool { return s.Count == 0 && s.Volumes.Count == 0 }

// CleanupReport is what a sweep actually removed.
type CleanupReport struct {
	Images     int
	Cache      int
	Containers int
	Networks   int
	Volumes    int
	Bytes      int64
	Failed     int // objects the daemon refused to remove

	// Incomplete marks a sweep that stopped while there was still something to
	// take — it ran out of passes or out of time. Everything counted above was
	// still removed; what is left needs another sweep.
	Incomplete bool
}

// Summary is the sentence the System page shows after a sweep.
func (r CleanupReport) Summary() string {
	var parts []string
	for _, p := range []struct {
		n    int
		one  string
		many string
	}{
		{r.Images, "image", "images"},
		{r.Cache, "build cache entry", "build cache entries"},
		{r.Containers, "container", "containers"},
		{r.Networks, "network", "networks"},
		{r.Volumes, "volume", "volumes"},
	} {
		if p.n == 0 {
			continue
		}
		word := p.many
		if p.n == 1 {
			word = p.one
		}
		parts = append(parts, fmt.Sprintf("%d %s", p.n, word))
	}
	if len(parts) == 0 {
		return "Nothing to clean up — Docker is already using only what it needs."
	}
	msg := fmt.Sprintf("Removed %s, freeing about %s.", strings.Join(parts, ", "), HumanSize(r.Bytes))
	if r.Failed == 1 {
		msg += " One object was still in use and was left alone."
	} else if r.Failed > 1 {
		msg += fmt.Sprintf(" %d objects were still in use and were left alone.", r.Failed)
	}
	// A sweep normally runs until there is nothing left to take, so saying this
	// only when it did not is what keeps the ordinary message meaning "done".
	if r.Incomplete {
		msg += " More was still coming free when the sweep reached its limit — run it again to finish."
	}
	return msg
}

// keep holds what a sweep must not touch.
//
// Everything Quasar can still act on has to survive: the container a stopped
// app is started from, the image a rollback would redeploy, the network a stack
// expects to find when it comes back up. The rules are gathered here rather
// than repeated per category, because a sweep is only as safe as its least
// careful exclusion.
type keep struct {
	apps     map[string]bool // app IDs the platform still knows about
	builds   map[string]bool // local build repositories of those apps
	projects map[string]bool // compose project names of those apps
	networks map[string]bool // networks the platform owns
	current  map[string]bool // container IDs an app is currently deployed as
}

func (c *Client) protect(appIDs []string, containers []*container.Summary) keep {
	k := keep{
		apps:     map[string]bool{},
		builds:   map[string]bool{},
		projects: map[string]bool{},
		networks: map[string]bool{c.network: true, c.socketNet: true},
		current:  map[string]bool{},
	}
	for _, id := range appIDs {
		k.apps[id] = true
		k.builds[buildTagPrefix(id)] = true
		k.projects[composeProject(id)] = true
	}

	// A deploy starts the replacement container before retiring the one it
	// replaces, so an app can own several containers at once and the newest is
	// the one every action resolves to. That newest container is what a stopped
	// app is started from and must survive; its predecessors are dead weight an
	// interrupted deploy left behind.
	newest := map[string]*container.Summary{}
	for _, ct := range containers {
		id := ct.Labels[appLabel]
		if id == "" || !k.apps[id] {
			continue
		}
		if cur, ok := newest[id]; !ok || ct.Created > cur.Created {
			newest[id] = ct
		}
	}
	for _, ct := range newest {
		k.current[ct.ID] = true
	}
	return k
}

func (k keep) container(ct *container.Summary) bool {
	switch ct.State {
	case "running", "paused", "restarting", "removing", "created":
		// "created" covers the few seconds between ContainerCreate and
		// ContainerStart during a deploy, which is exactly when a sweep would
		// be most damaging.
		return true
	}
	// The platform's own containers, including the exited quasar-updater a
	// self-update deliberately leaves behind as its only post-mortem record.
	for _, name := range ct.Names {
		if strings.HasPrefix(strings.TrimPrefix(name, "/"), "quasar-") {
			return true
		}
	}
	if k.current[ct.ID] {
		return true
	}
	// A compose stack is managed as a whole: a service of it that happens to be
	// stopped is part of the app, not a leftover.
	return k.projects[ct.Labels["com.docker.compose.project"]]
}

func (k keep) image(img *image.Summary) bool {
	// Containers counts stopped containers too, which is what makes a stopped
	// app's image safe from here.
	if img.Containers != 0 {
		return true
	}
	// Git builds are tagged qs-<app>:<unix>, and the last few of them are what a
	// rollback redeploys — deploy already caps how many are kept. Removing them
	// here would quietly take the rollback targets with them. Only while the app
	// exists, though: once it is deleted nothing can ever redeploy them, and
	// they are among the largest things left on a build server.
	for _, tag := range img.RepoTags {
		if repo, _, ok := strings.Cut(tag, ":"); ok && k.builds[repo] {
			return true
		}
	}
	return false
}

func (k keep) volume(v *volume.Volume) bool {
	// RefCount counts stopped containers, and -1 means the daemon did not work
	// it out — neither is a volume anyone may delete.
	if v.UsageData == nil || v.UsageData.RefCount != 0 {
		return true
	}
	return k.projects[v.Labels["com.docker.compose.project"]]
}

func (k keep) network(n network.Summary) bool {
	// The three networks every daemon is born with cannot be removed anyway.
	switch n.Name {
	case "bridge", "host", "none":
		return true
	}
	if n.Scope != "local" || k.networks[n.Name] || strings.HasPrefix(n.Name, "quasar-") {
		return true
	}
	// A compose network is recreated on the next `up`, but only if compose still
	// has an app to bring up. Network inspection reports connected containers,
	// and a stopped container is not connected — without this the network of
	// every stopped stack would look abandoned.
	return k.projects[n.Labels["com.docker.compose.project"]]
}

// sweep is everything a cleanup would remove, resolved in one pass so the
// preview on the page and the removal the button performs cannot disagree.
type sweep struct {
	images     []*image.Summary
	dangling   []*image.Summary
	cache      []*build.CacheRecord
	containers []*container.Summary
	networks   []string
	volumes    []*volume.Volume
	usage      DiskUsage
}

// empty reports whether there is nothing here to remove. A pass that finds this
// is the pass that ends a sweep — everything else is a bound on how long it may
// keep trying.
func (s sweep) empty() bool {
	return len(s.images) == 0 && len(s.dangling) == 0 && len(s.cache) == 0 &&
		len(s.containers) == 0 && len(s.networks) == 0 && len(s.volumes) == 0
}

// resolve works out what Docker is holding and which of it is disposable.
//
// It leans on /system/df for everything but networks: that is the one endpoint
// where the daemon fills in the reference counts and shared-layer sizes this
// has to be correct about, and doing it in a single call keeps the preview
// consistent with itself.
func (c *Client) resolve(ctx context.Context, appIDs []string) (sweep, error) {
	var s sweep
	du, err := c.api.DiskUsage(ctx, types.DiskUsageOptions{})
	if err != nil {
		return s, err
	}
	k := c.protect(appIDs, du.Containers)
	s.usage = usageOf(du)

	for _, img := range du.Images {
		if k.image(img) {
			continue
		}
		if untagged(img) {
			s.dangling = append(s.dangling, img)
		} else {
			s.images = append(s.images, img)
		}
	}
	for _, rec := range du.BuildCache {
		if !rec.Shared && !rec.InUse {
			s.cache = append(s.cache, rec)
		}
	}
	for _, ct := range du.Containers {
		if !k.container(ct) {
			s.containers = append(s.containers, ct)
		}
	}
	for _, v := range du.Volumes {
		if !k.volume(v) {
			s.volumes = append(s.volumes, v)
		}
	}
	s.networks = c.strayNetworks(ctx, k)
	return s, nil
}

// usageOf reduces the daemon's disk report to the totals the System page shows.
// Both the scan and the measurement of what a sweep freed go through it, so the
// before and after of a sweep are counted the same way and their difference
// means something.
func usageOf(du types.DiskUsage) DiskUsage {
	u := DiskUsage{
		ImagesCount:     len(du.Images),
		ImagesBytes:     du.LayersSize,
		ContainersCount: len(du.Containers),
		VolumesCount:    len(du.Volumes),
		CacheCount:      len(du.BuildCache),
	}
	for _, rec := range du.BuildCache {
		// A shared record is one the daemon also counts under another; adding it
		// here would charge the same layer to the total twice. This is the same
		// arithmetic `docker system df` does.
		if !rec.Shared {
			u.CacheBytes += rec.Size
		}
	}
	for _, ct := range du.Containers {
		u.ContainersBytes += ct.SizeRw
	}
	for _, v := range du.Volumes {
		if v.UsageData != nil && v.UsageData.Size > 0 {
			u.VolumesBytes += v.UsageData.Size
		}
	}
	return u
}

// strayNetworks lists local networks nothing is attached to and the platform
// does not own. Each candidate is inspected because a network listing does not
// report its endpoints, and a network wrongly judged unused takes a live
// application off the internet.
func (c *Client) strayNetworks(ctx context.Context, k keep) []string {
	list, err := c.api.NetworkList(ctx, network.ListOptions{})
	if err != nil {
		return nil
	}
	var out []string
	for _, n := range list {
		if k.network(n) {
			continue
		}
		full, err := c.api.NetworkInspect(ctx, n.ID, network.InspectOptions{})
		if err != nil || len(full.Containers) > 0 {
			continue
		}
		out = append(out, n.Name)
	}
	sort.Strings(out)
	return out
}

// untagged reports whether an image is only reachable by its digest — the
// `<none>` layers a rebuild leaves behind when it moves a tag onto a new image.
func untagged(img *image.Summary) bool {
	for _, tag := range img.RepoTags {
		if tag != "<none>:<none>" {
			return false
		}
	}
	return true
}

// firstTag names a tagged image for the operator. An image can hold a `<none>`
// entry alongside real ones, and that is the one name that says nothing.
func firstTag(img *image.Summary) string {
	for _, tag := range img.RepoTags {
		if tag != "<none>:<none>" {
			return tag
		}
	}
	return img.ID
}

// imageBytes is what removing an image actually gives back: its own layers,
// minus the ones another image still holds.
func imageBytes(img *image.Summary) int64 {
	if img.SharedSize > 0 {
		return img.Size - img.SharedSize
	}
	return img.Size
}

// Storage is the disk picture the System page shows: what Docker is using, and
// how much of it a sweep would give back.
type Storage struct {
	Usage   DiskUsage
	Cleanup CleanupScan
}

func (c *Client) Storage(ctx context.Context, appIDs []string) (Storage, error) {
	s, err := c.resolve(ctx, appIDs)
	if err != nil {
		return Storage{}, err
	}
	return Storage{Usage: s.usage, Cleanup: s.scan()}, nil
}

func (s sweep) scan() CleanupScan {
	var out CleanupScan

	add := func(r Reclaimable) {
		if r.Count == 0 {
			return
		}
		out.Items = append(out.Items, r)
		out.Count += r.Count
		out.Bytes += r.Bytes
	}

	var names []string
	var bytes int64
	var objects []ReclaimableObject
	for _, img := range s.images {
		names = append(names, firstTag(img))
		bytes += imageBytes(img)
		objects = append(objects, ReclaimableObject{
			ID:    img.ID,
			Name:  firstTag(img),
			Note:  otherTags(img) + since(time.Unix(img.Created, 0)),
			Bytes: imageBytes(img),
		})
	}
	add(Reclaimable{
		Key:     "images",
		Label:   "Images no container uses",
		Count:   len(s.images),
		Bytes:   bytes,
		Note:    examples(names),
		Objects: listed(objects),
	})

	bytes, objects = 0, nil
	for _, img := range s.dangling {
		bytes += imageBytes(img)
		objects = append(objects, ReclaimableObject{
			ID: img.ID,
			// An untagged image has no name left; its ID is the only thing it
			// can be told apart by, and the short form is the one the daemon
			// shows everywhere else.
			Name:  shortID(img.ID),
			Note:  since(time.Unix(img.Created, 0)),
			Bytes: imageBytes(img),
		})
	}
	add(Reclaimable{
		Key:     "dangling",
		Label:   "Untagged layers left by rebuilds",
		Count:   len(s.dangling),
		Bytes:   bytes,
		Objects: listed(objects),
	})

	bytes, objects = 0, nil
	for _, rec := range s.cache {
		bytes += rec.Size
		objects = append(objects, ReclaimableObject{
			ID:    rec.ID,
			Name:  shortID(rec.ID),
			Note:  strings.TrimSpace(rec.Description + " " + since(rec.CreatedAt)),
			Bytes: rec.Size,
		})
	}
	add(Reclaimable{
		Key:     "cache",
		Label:   "Build cache no longer referenced",
		Count:   len(s.cache),
		Bytes:   bytes,
		Objects: listed(objects),
		// The daemon prunes the build cache or leaves it; there is no call that
		// removes one record.
		WholeOnly: true,
	})

	names, bytes, objects = nil, 0, nil
	for _, ct := range s.containers {
		name := ""
		if len(ct.Names) > 0 {
			name = strings.TrimPrefix(ct.Names[0], "/")
			names = append(names, name)
		}
		bytes += ct.SizeRw
		objects = append(objects, ReclaimableObject{
			ID:    ct.ID,
			Name:  name,
			Note:  ct.Image + " · " + ct.Status,
			Bytes: ct.SizeRw,
		})
	}
	add(Reclaimable{
		Key:     "containers",
		Label:   "Stopped containers nothing owns",
		Count:   len(s.containers),
		Bytes:   bytes,
		Note:    examples(names),
		Objects: listed(objects),
	})

	objects = nil
	for _, n := range s.networks {
		objects = append(objects, ReclaimableObject{ID: n, Name: n})
	}
	add(Reclaimable{
		Key:     "networks",
		Label:   "Networks with nothing attached",
		Count:   len(s.networks),
		Note:    examples(s.networks),
		Objects: listed(objects),
	})

	names, bytes, objects = nil, 0, nil
	for _, v := range s.volumes {
		names = append(names, v.Name)
		var size int64
		if v.UsageData != nil {
			size = v.UsageData.Size
			bytes += size
		}
		objects = append(objects, ReclaimableObject{
			ID: v.Name, Name: v.Name, Note: createdAt(v.CreatedAt), Bytes: size,
		})
	}
	out.Volumes = Reclaimable{
		Key:     "volumes",
		Label:   "Volumes no container references",
		Count:   len(s.volumes),
		Bytes:   bytes,
		Note:    examples(names),
		Objects: listed(objects),
	}
	return out
}

// listed puts the biggest first — reclaiming space is why anyone opens this —
// and cuts the list at what a page can usefully show.
func listed(objects []ReclaimableObject) []ReclaimableObject {
	sort.SliceStable(objects, func(i, j int) bool {
		if objects[i].Bytes != objects[j].Bytes {
			return objects[i].Bytes > objects[j].Bytes
		}
		return objects[i].Name < objects[j].Name
	})
	if len(objects) > objectsListed {
		return objects[:objectsListed]
	}
	return objects
}

// shortID is an ID as the daemon prints it: the twelve hex characters after the
// algorithm, which are enough to recognise one object among a page of them.
func shortID(id string) string {
	if _, hex, ok := strings.Cut(id, ":"); ok {
		id = hex
	}
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// otherTags names the tags beyond the one shown, so an image kept for a second
// name is not mistaken for one that would vanish entirely.
func otherTags(img *image.Summary) string {
	first := firstTag(img)
	var rest []string
	for _, tag := range img.RepoTags {
		if tag != first && tag != "<none>:<none>" {
			rest = append(rest, tag)
		}
	}
	if len(rest) == 0 {
		return ""
	}
	return "also " + strings.Join(rest, ", ") + " · "
}

// since is how long ago something was made, for telling one untagged layer from
// another. A zero time yields nothing rather than a date in 1970.
func since(t time.Time) string {
	if t.IsZero() || t.Unix() <= 0 {
		return ""
	}
	return humanDuration(time.Since(t)) + " old"
}

// createdAt reads the timestamp the volume API reports as a string.
func createdAt(s string) string {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return ""
	}
	return since(t)
}

// examples names the first few objects in a category. A count alone asks the
// operator to trust the tool; three names let them recognise what is about to
// go and stop if it is something they wanted.
func examples(names []string) string {
	sort.Strings(names)
	const show = 3
	if len(names) <= show {
		return strings.Join(names, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(names[:show], ", "), len(names)-show)
}

// Selection is what the operator ticked on the cleanup card: whole categories,
// individual objects, or — the ordinary case — nothing at all.
//
// The empty selection means everything the scan found. That is what makes the
// button work the way it always has when no box is ticked, without every caller
// having to say so.
type Selection struct {
	categories map[string]bool // "<key>"
	objects    map[string]bool // "<key>:<id>"
}

// ParseSelection reads the values the cleanup card submits: "<key>:<id>" for
// one object and "<key>:*" for a whole category.
//
// Anything malformed is dropped rather than guessed at. A selection is a list
// of things to delete, so a value that cannot be read has to become no
// permission at all, never a broader one.
func ParseSelection(values []string) Selection {
	sel := Selection{categories: map[string]bool{}, objects: map[string]bool{}}
	for _, v := range values {
		key, id, ok := strings.Cut(v, ":")
		if !ok || key == "" || id == "" {
			continue
		}
		if id == "*" {
			sel.categories[key] = true
			continue
		}
		sel.objects[v] = true
	}
	return sel
}

// Empty reports whether nothing was ticked, which is the whole scan.
func (s Selection) Empty() bool { return len(s.categories) == 0 && len(s.objects) == 0 }

// wants reports whether one object is in the selection.
func (s Selection) wants(key, id string) bool {
	return s.Empty() || s.categories[key] || s.objects[key+":"+id]
}

// wantsWhole reports whether a category that can only go as a whole is in.
// Ticking one of its lines cannot stand in for this: there is no call that
// would remove only that line.
func (s Selection) wantsWhole(key string) bool { return s.Empty() || s.categories[key] }

// CleanupOptions is what a sweep was asked to do.
type CleanupOptions struct {
	// Volumes includes every orphaned volume. It is the consent the card asks
	// for separately, because a volume is the one thing here that cannot be
	// pulled or rebuilt back.
	Volumes bool
	// Only narrows the sweep to what was ticked. The zero value is everything.
	Only Selection
}

// narrow reduces a resolved sweep to what was actually asked for.
//
// Applied to every pass, not just the first: a narrowed sweep must not widen
// into whatever the cascade uncovered. Removing a ticked container frees the
// image it was created from, and that image is not a thing the operator ticked.
func (o CleanupOptions) narrow(s sweep) sweep {
	out := sweep{usage: s.usage}
	for _, img := range s.images {
		if o.Only.wants("images", img.ID) {
			out.images = append(out.images, img)
		}
	}
	for _, img := range s.dangling {
		if o.Only.wants("dangling", img.ID) {
			out.dangling = append(out.dangling, img)
		}
	}
	if o.Only.wantsWhole("cache") {
		out.cache = s.cache
	}
	for _, ct := range s.containers {
		if o.Only.wants("containers", ct.ID) {
			out.containers = append(out.containers, ct)
		}
	}
	for _, n := range s.networks {
		if o.Only.wants("networks", n) {
			out.networks = append(out.networks, n)
		}
	}
	// Volumes are the one category a sweep of "everything" leaves alone, so an
	// empty selection never reaches them — only the separate consent above, or
	// a volume ticked by name, which is that same consent given one line at a
	// time.
	for _, v := range s.volumes {
		if o.Volumes || (!o.Only.Empty() && o.Only.wants("volumes", v.Name)) {
			out.volumes = append(out.volumes, v)
		}
	}
	return out
}

// What ends a sweep is a look that finds nothing left to take. These only bound
// how long it may go on trying.
//
// Removal cascades. The container removed in one pass is what frees the image
// it was created from; the image removed by its last tag is what turns its
// layers into the untagged leftovers of the next, and those are what release
// the build cache records underneath. A single pass therefore always stops
// short of what it set out to do, and the page it returns to says so — a sweep
// reporting success over a breakdown still offering a few hundred megabytes.
//
// The chain is three or four deep in the ordinary case, which is what the bound
// used to be — and a bound set to the ordinary case is reached by every server
// that is not the ordinary case, which is how a full cleanup came to take six
// presses of the button with nothing done in between. It is a backstop now:
// high enough that reaching it means something is regenerating faster than this
// removes it, at which point stopping is the right answer anyway. The real
// budget is the caller's deadline, which every pass is subject to.
const cleanupPasses = 32

// cleanupSettle is how long a pass that took nothing waits before looking once
// more. The daemon does not always finish with an object by the time it says it
// has — a container still winding down keeps its image out of the next scan —
// and concluding "nothing left" from that one look is what left work behind for
// the next press of the button.
const cleanupSettle = 3 * time.Second

// cleanupStalls is how many passes in a row may take nothing before the sweep
// concludes that what is left is not going. Two rather than one: the first is
// what asks the question, the second — after the pause above — is what answers
// it.
const cleanupStalls = 2

// Cleanup removes everything a scan found disposable, and the orphaned volumes
// too if the operator asked for those separately. It repeats until a pass finds
// nothing left to take, so what the operator is shown afterwards is a sweep
// that finished rather than one that ran out of turns.
//
// Every removal is attempted on its own rather than through Docker's own prune
// endpoints, which take no exclusions: the whole difference between this and
// `docker system prune -a` is what it refuses to touch.
func (c *Client) Cleanup(ctx context.Context, appIDs []string, opts CleanupOptions) (CleanupReport, error) {
	before, err := c.api.DiskUsage(ctx, types.DiskUsageOptions{})
	if err != nil {
		return CleanupReport{}, err
	}

	var rep CleanupReport
	var progress bool // some pass removed something
	var stalled int   // passes in a row that removed nothing
	pass := 0
	for ; pass < cleanupPasses; pass++ {
		s, err := c.resolve(ctx, appIDs)
		if err != nil {
			if pass == 0 {
				return CleanupReport{}, err
			}
			// What the earlier passes removed still stands and is reported, but
			// this is not a sweep that finished.
			rep.Incomplete = true
			break
		}
		left := opts.narrow(s)
		var removed, failed int
		if !left.empty() {
			removed, failed = c.sweepOnce(ctx, left, &rep)
			// Only the latest pass's failures are kept: an object an earlier
			// pass could not remove was tried again, and either went or is
			// counted here. Summing them would report one stubborn image as
			// four.
			rep.Failed = failed
		}
		if removed > 0 {
			progress = true
			stalled = 0
			continue
		}

		// Nothing went this time: either there is nothing left, or what is left
		// refuses to go, or the daemon has not caught up with the pass before.
		// Only the first of those is a finished sweep, and one look cannot tell
		// them apart — so unless nothing has gone all sweep, it waits and asks
		// again rather than handing back a page with work still on it.
		stalled++
		if !progress || stalled >= cleanupStalls {
			break
		}
		if !settle(ctx) {
			rep.Incomplete = true
			break
		}
	}
	// Running out of passes means the cascade was still going, which is the one
	// thing the operator has to be told: the button is worth pressing again.
	if pass == cleanupPasses {
		rep.Incomplete = true
	}

	// Measured rather than added up. The per-object sizes are estimates that
	// double-count shared layers and credit an image whose removal only dropped
	// one of its tags, which is how a sweep came to claim more than the disk
	// gave back. A negative difference means something else grew while this ran
	// — a deploy, most likely — and is not this sweep's to report.
	if after, err := c.api.DiskUsage(ctx, types.DiskUsageOptions{}); err == nil {
		if freed := usageOf(before).Total() - usageOf(after).Total(); freed > 0 {
			rep.Bytes = freed
		}
	}
	return rep, nil
}

// settle pauses between two looks at the daemon and reports whether it was
// allowed to finish. A sweep that has run out of time stops asking rather than
// waiting out a deadline it has already passed.
func settle(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(cleanupSettle):
		return true
	}
}

// sweepOnce removes everything one resolution found disposable, adding what
// went to rep. It returns how much it removed and how much the daemon refused,
// which is what tells the caller whether another pass is worth making.
func (c *Client) sweepOnce(ctx context.Context, s sweep, rep *CleanupReport) (removed, failed int) {
	for _, ct := range s.containers {
		if err := c.api.ContainerRemove(ctx, ct.ID, container.RemoveOptions{}); err != nil {
			failed++
			continue
		}
		rep.Containers++
		removed++
	}

	for _, img := range append(append([]*image.Summary{}, s.images...), s.dangling...) {
		if c.removeImage(ctx, img) {
			rep.Images++
			removed++
			continue
		}
		failed++
	}

	// The build cache has no per-record delete, and its own prune already keeps
	// what is in use — the same set the scan counted.
	if len(s.cache) > 0 {
		if out, err := c.api.BuildCachePrune(ctx, build.CachePruneOptions{All: true}); err == nil {
			rep.Cache += len(out.CachesDeleted)
			removed += len(out.CachesDeleted)
		} else {
			failed++
		}
	}

	for _, name := range s.networks {
		if err := c.api.NetworkRemove(ctx, name); err != nil {
			failed++
			continue
		}
		rep.Networks++
		removed++
	}

	// Already narrowed to the volumes the operator consented to, if any.
	for _, v := range s.volumes {
		if err := c.api.VolumeRemove(ctx, v.Name, false); err != nil {
			failed++
			continue
		}
		rep.Volumes++
		removed++
	}
	return removed, failed
}

// removeImage deletes an image and reports whether the daemon took it. How it
// has to be asked depends on whether the image still carries a name.
func (c *Client) removeImage(ctx context.Context, img *image.Summary) bool {
	if untagged(img) {
		// An untagged image has no name to delete it by, only its ID — and the
		// daemon refuses an ID that is still referenced somewhere, which for one
		// of these is a digest left over from the pull or rebuild that untagged
		// it. That refusal is what kept "untagged layers left by rebuilds" on
		// the page after every sweep. Forcing is what `docker image prune`
		// itself does here, and it is safe for the same reason: an image any
		// container still uses never reaches this, keep.image holds it back.
		_, err := c.api.ImageRemove(ctx, img.ID, image.RemoveOptions{Force: true, PruneChildren: true})
		return err == nil
	}
	// A tagged image is deleted the way `docker rmi` does it: by each of its
	// tags, so the daemon drops the references first and the image with the
	// last one. Deleting a multi-tagged image by ID would need forcing, and
	// forcing it there would defeat the daemon's own last check that nothing is
	// using it.
	ok := false
	for _, ref := range img.RepoTags {
		if _, err := c.api.ImageRemove(ctx, ref, image.RemoveOptions{PruneChildren: true}); err == nil {
			ok = true
		}
	}
	return ok
}

// HumanSize formats a byte count the way the pages show sizes: never more than
// three significant figures, and never a unit the number does not fill.
func HumanSize(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit && exp < 3; n /= unit {
		div *= unit
		exp++
	}
	v := float64(b) / float64(div)
	if v >= 100 {
		return fmt.Sprintf("%.0f %cB", v, "KMGT"[exp])
	}
	return fmt.Sprintf("%.1f %cB", v, "KMGT"[exp])
}
