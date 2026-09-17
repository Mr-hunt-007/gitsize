package analyze

import (
	"container/heap"
	"path"
	"sort"
	"strings"
	"time"
)

// SortKey selects which size ranks results.
type SortKey string

const (
	SortDisk SortKey = "disk" // on-disk (compressed, delta) size
	SortSize SortKey = "size" // uncompressed size
)

// Blob is one blob version found in history.
type Blob struct {
	OID  string
	Path string
	Size int64
	Disk int64
}

// Group is an aggregate over many blobs (a path, an extension, a directory).
type Group struct {
	Key   string
	Blobs int64
	Size  int64
	Disk  int64
}

func (g *Group) add(b Blob) {
	g.Blobs++
	g.Size += b.Size
	g.Disk += b.Disk
}

func key(k SortKey, size, disk int64) int64 {
	if k == SortSize {
		return size
	}
	return disk
}

// less orders "a ranks before b": bigger first, then deterministic tiebreaks.
func blobLess(k SortKey, a, b Blob) bool {
	ka, kb := key(k, a.Size, a.Disk), key(k, b.Size, b.Disk)
	if ka != kb {
		return ka > kb
	}
	if a.Path != b.Path {
		return a.Path < b.Path
	}
	return a.OID < b.OID
}

func groupLess(k SortKey, a, b Group) bool {
	ka, kb := key(k, a.Size, a.Disk), key(k, b.Size, b.Disk)
	if ka != kb {
		return ka > kb
	}
	return a.Key < b.Key
}

// blobHeap is a min-heap by rank so the smallest of the current top N is on top.
type blobHeap struct {
	k     SortKey
	items []Blob
}

func (h *blobHeap) Len() int           { return len(h.items) }
func (h *blobHeap) Less(i, j int) bool { return blobLess(h.k, h.items[j], h.items[i]) }
func (h *blobHeap) Swap(i, j int)      { h.items[i], h.items[j] = h.items[j], h.items[i] }
func (h *blobHeap) Push(x any)         { h.items = append(h.items, x.(Blob)) }
func (h *blobHeap) Pop() any {
	old := h.items
	x := old[len(old)-1]
	h.items = old[:len(old)-1]
	return x
}

// Aggregator consumes blobs one at a time and keeps only what the report needs:
// the top N blobs plus per-path, per-extension and per-directory totals.
type Aggregator struct {
	k     SortKey
	n     int
	top   blobHeap
	Total Group
	paths map[string]*Group
	exts  map[string]*Group
	dirs  map[string]*Group
}

// NewAggregator keeps the top n blobs ranked by k.
func NewAggregator(k SortKey, n int) *Aggregator {
	return &Aggregator{
		k:     k,
		n:     n,
		top:   blobHeap{k: k},
		paths: map[string]*Group{},
		exts:  map[string]*Group{},
		dirs:  map[string]*Group{},
	}
}

// Add records one blob.
func (a *Aggregator) Add(b Blob) {
	a.Total.add(b)
	addTo(a.paths, b.Path, b)
	addTo(a.exts, Ext(b.Path), b)
	addTo(a.dirs, Dir(b.Path), b)
	if a.n <= 0 {
		return
	}
	if a.top.Len() < a.n {
		heap.Push(&a.top, b)
		return
	}
	if blobLess(a.k, b, a.top.items[0]) {
		a.top.items[0] = b
		heap.Fix(&a.top, 0)
	}
}

func addTo(m map[string]*Group, k string, b Blob) {
	g := m[k]
	if g == nil {
		g = &Group{Key: k}
		m[k] = g
	}
	g.add(b)
}

// TopBlobs returns the largest blobs, biggest first.
func (a *Aggregator) TopBlobs() []Blob {
	out := append([]Blob{}, a.top.items...)
	sort.Slice(out, func(i, j int) bool { return blobLess(a.k, out[i], out[j]) })
	return out
}

// Paths returns every path group, biggest first.
func (a *Aggregator) Paths() []Group { return sortedGroups(a.k, a.paths) }

// Exts returns every extension group, biggest first.
func (a *Aggregator) Exts() []Group { return sortedGroups(a.k, a.exts) }

// Dirs returns every directory group, biggest first.
func (a *Aggregator) Dirs() []Group { return sortedGroups(a.k, a.dirs) }

func sortedGroups(k SortKey, m map[string]*Group) []Group {
	out := make([]Group, 0, len(m))
	for _, g := range m {
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool { return groupLess(k, out[i], out[j]) })
	return out
}

// NoExt is the extension bucket for files without one.
const NoExt = "(none)"

// RootDir is the directory bucket for files at the repository root.
const RootDir = "(root)"

// Ext returns the lower-cased extension of a git path, or NoExt. Dotfiles such
// as ".gitignore" have no extension.
func Ext(p string) string {
	base := path.Base(p)
	trimmed := strings.TrimLeft(base, ".")
	e := path.Ext(trimmed)
	if e == "" || e == "." {
		return NoExt
	}
	return strings.ToLower(e)
}

// Dir returns the directory containing a git path, or RootDir.
func Dir(p string) string {
	i := strings.LastIndexByte(p, '/')
	if i <= 0 {
		return RootDir
	}
	return p[:i]
}

// Intro is the earliest commit (by committer time) seen introducing something.
type Intro struct {
	Commit string
	Time   int64
}

// Earlier reports whether (commit, t) should replace cur.
func (cur Intro) Earlier(commit string, t int64) bool {
	if cur.Commit == "" {
		return true
	}
	if t != cur.Time {
		return t < cur.Time
	}
	return commit < cur.Commit
}

// Month is one bar of the growth chart.
type Month struct {
	Month string `json:"month"` // YYYY-MM, UTC
	Blobs int64  `json:"blobs"`
	Size  int64  `json:"size"`
	Disk  int64  `json:"disk"`
}

// MonthOf formats a unix time as YYYY-MM in UTC.
func MonthOf(t int64) string { return time.Unix(t, 0).UTC().Format("2006-01") }

// BuildHistory buckets blobs by the month of their first commit. Months with no
// new blobs between the first and last are included with zero values. sizes
// maps blob oid to its sizes; first maps blob oid to the earliest commit time.
// It returns the months and the totals of blobs never seen in any commit diff.
func BuildHistory(sizes map[string][2]int64, first map[string]int64) ([]Month, Group) {
	buckets := map[string]*Month{}
	var unattributed Group
	for oid, sz := range sizes {
		t, ok := first[oid]
		if !ok {
			unattributed.Blobs++
			unattributed.Size += sz[0]
			unattributed.Disk += sz[1]
			continue
		}
		m := MonthOf(t)
		b := buckets[m]
		if b == nil {
			b = &Month{Month: m}
			buckets[m] = b
		}
		b.Blobs++
		b.Size += sz[0]
		b.Disk += sz[1]
	}
	if len(buckets) == 0 {
		return nil, unattributed
	}
	keys := make([]string, 0, len(buckets))
	for k := range buckets {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	start, _ := time.Parse("2006-01", keys[0])
	end, _ := time.Parse("2006-01", keys[len(keys)-1])
	var out []Month
	for d := start; !d.After(end); d = d.AddDate(0, 1, 0) {
		k := d.Format("2006-01")
		if b := buckets[k]; b != nil {
			out = append(out, *b)
		} else {
			out = append(out, Month{Month: k})
		}
	}
	return out, unattributed
}
