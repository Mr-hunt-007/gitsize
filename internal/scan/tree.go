package scan

import (
	"sort"
	"strings"

	"github.com/Mr-hunt-007/gitsize/internal/analyze"
)

// TreeOptions asks the scan to also build the weight tree drawn by --svg.
type TreeOptions struct {
	Depth   int // levels below the root; 0 means unlimited
	Top     int // entries at the first level before "+N more"; 0 means unlimited
	DeepTop int // entries per directory below the first level; 0 means unlimited
}

// NoPath names blobs that rev-list reached without a path (for example a
// blob tagged directly).
const NoPath = "(no path)"

// Tree is where the repository's history weight lives: every blob version in
// history summed by path and by directory. It is not part of the JSON shape.
type Tree struct {
	Root  *TreeNode
	Depth int
	// Split sums blob versions by their status relative to HEAD, in the
	// order in HEAD, old version, deleted, unknown.
	Split [4]SplitEntry
}

// SplitEntry is one slice of the history weight strip.
type SplitEntry struct {
	Status string
	Blobs  int64
	Size   int64
	Disk   int64
}

// Status indexes into Tree.Split and TreeNode.StatusDisk.
const (
	splitInHead = iota
	splitOld
	splitDeleted
	splitUnknown
)

var splitStatus = [4]string{StatusInHead, StatusOldVersion, StatusDeleted, StatusUnknown}

// TreeNode is a directory or a file path with every version in history summed.
type TreeNode struct {
	Name     string
	Path     string // "" for the root
	Dir      bool
	Paths    int64 // distinct file paths; 1 for a file
	Versions int64 // blob versions
	Size     int64
	Disk     int64
	// Status is StatusInHead when the path (or something under the
	// directory) exists in HEAD, StatusDeleted when it does not, and
	// StatusUnknown when HEAD does not resolve.
	Status string
	// StatusDisk and StatusBlobs split Disk and Versions by blob status:
	// in HEAD, old version, deleted, unknown.
	StatusDisk  [4]int64
	StatusBlobs [4]int64
	Introduced  *Introduced // files only
	Children    []*TreeNode
	Hidden      int  // entries cut by the Top limits
	Collapsed   bool // a directory at the depth limit whose contents are not drawn
}

// Value is the node's weight for the sort key.
func (n *TreeNode) Value(k analyze.SortKey) int64 {
	if k == analyze.SortSize {
		return n.Size
	}
	return n.Disk
}

type pathWeight struct {
	versions   int64
	size, disk int64
	statDisk   [4]int64
	statBlobs  [4]int64
}

type trieDir struct {
	dirs  map[string]*trieDir
	files map[string]*pathWeight
	total TreeNode // aggregate of everything below, without children
}

func splitIndex(status string) int {
	for i, s := range splitStatus {
		if s == status {
			return i
		}
	}
	return splitUnknown
}

// buildTree turns per-path weights into the drawn tree. pathStatus and
// dirStatus give the HEAD status of a file path and a directory path.
func buildTree(weights map[string]*pathWeight, k analyze.SortKey, o TreeOptions, name string,
	pathStatus, dirStatus func(string) string) *TreeNode {
	root := &trieDir{}
	for p, w := range weights {
		parts := []string{NoPath}
		if p != "" {
			parts = strings.Split(p, "/")
		}
		d := root
		for _, part := range parts[:len(parts)-1] {
			if d.dirs == nil {
				d.dirs = map[string]*trieDir{}
			}
			c := d.dirs[part]
			if c == nil {
				c = &trieDir{}
				d.dirs[part] = c
			}
			d = c
		}
		if d.files == nil {
			d.files = map[string]*pathWeight{}
		}
		d.files[parts[len(parts)-1]] = w
	}
	sumTrie(root)

	var build func(d *trieDir, name, path string, level int) *TreeNode
	build = func(d *trieDir, name, path string, level int) *TreeNode {
		n := d.total
		n.Name, n.Path, n.Dir = name, path, true
		n.Status = dirStatus(path)
		if len(d.dirs)+len(d.files) == 0 {
			return &n
		}
		if o.Depth > 0 && level >= o.Depth {
			n.Collapsed = true
			return &n
		}
		type entry struct {
			name string
			dir  *trieDir
			file *pathWeight
			val  int64
		}
		var entries []entry
		for nm, c := range d.dirs {
			v := c.total.Disk
			if k == analyze.SortSize {
				v = c.total.Size
			}
			entries = append(entries, entry{name: nm, dir: c, val: v})
		}
		for nm, w := range d.files {
			v := w.disk
			if k == analyze.SortSize {
				v = w.size
			}
			entries = append(entries, entry{name: nm, file: w, val: v})
		}
		sort.Slice(entries, func(i, j int) bool {
			a, b := entries[i], entries[j]
			if a.val != b.val {
				return a.val > b.val
			}
			if a.name != b.name {
				return a.name < b.name
			}
			return a.dir != nil && b.dir == nil
		})
		top := o.Top
		if level >= 1 && o.DeepTop > 0 && (top == 0 || top > o.DeepTop) {
			top = o.DeepTop
		}
		if top > 0 && len(entries) > top {
			n.Hidden = len(entries) - top
			entries = entries[:top]
		}
		for _, e := range entries {
			cp := e.name
			if path != "" {
				cp = path + "/" + e.name
			}
			if e.dir != nil {
				n.Children = append(n.Children, build(e.dir, e.name, cp, level+1))
				continue
			}
			gitPath := cp
			if cp == NoPath {
				gitPath = ""
			}
			n.Children = append(n.Children, &TreeNode{
				Name: e.name, Path: cp, Paths: 1, Versions: e.file.versions,
				Size: e.file.size, Disk: e.file.disk, Status: pathStatus(gitPath),
				StatusDisk: e.file.statDisk, StatusBlobs: e.file.statBlobs,
			})
		}
		return &n
	}
	return build(root, name, "", 0)
}

func sumTrie(d *trieDir) {
	t := &d.total
	for _, w := range d.files {
		t.Paths++
		t.Versions += w.versions
		t.Size += w.size
		t.Disk += w.disk
		for i := range w.statDisk {
			t.StatusDisk[i] += w.statDisk[i]
			t.StatusBlobs[i] += w.statBlobs[i]
		}
	}
	for _, c := range d.dirs {
		sumTrie(c)
		t.Paths += c.total.Paths
		t.Versions += c.total.Versions
		t.Size += c.total.Size
		t.Disk += c.total.Disk
		for i := range c.total.StatusDisk {
			t.StatusDisk[i] += c.total.StatusDisk[i]
			t.StatusBlobs[i] += c.total.StatusBlobs[i]
		}
	}
}

// Files calls fn for every file node drawn in the tree.
func (n *TreeNode) Files(fn func(*TreeNode)) {
	if !n.Dir {
		fn(n)
		return
	}
	for _, c := range n.Children {
		c.Files(fn)
	}
}
