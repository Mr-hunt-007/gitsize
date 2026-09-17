package scan

import (
	"strings"
	"testing"

	"github.com/Mr-hunt-007/gitsize/internal/analyze"
)

func w(versions, size, disk int64, status int) *pathWeight {
	p := &pathWeight{versions: versions, size: size, disk: disk}
	p.statDisk[status] = disk
	p.statBlobs[status] = versions
	return p
}

func names(n *TreeNode) string {
	var s []string
	for _, c := range n.Children {
		s = append(s, c.Name)
	}
	return strings.Join(s, ",")
}

func TestBuildTreeOrderTopAndDepth(t *testing.T) {
	weights := map[string]*pathWeight{
		"big.bin":         w(1, 900, 500, splitDeleted),
		"src/a.go":        w(3, 300, 100, splitInHead),
		"src/b.go":        w(1, 50, 40, splitInHead),
		"src/deep/x/y.go": w(1, 10, 10, splitInHead),
		"small.txt":       w(1, 5, 5, splitInHead),
		"mid.txt":         w(2, 1000, 60, splitOld),
		"":                w(1, 7, 7, splitUnknown),
	}
	inHead := map[string]bool{"src/a.go": true, "src/b.go": true, "src/deep/x/y.go": true, "small.txt": true, "mid.txt": true}
	pathStatus := func(p string) string {
		if inHead[p] {
			return StatusInHead
		}
		return StatusDeleted
	}
	dirStatus := func(p string) string {
		if p == "" || strings.HasPrefix("src/deep/x", p) {
			return StatusInHead
		}
		return StatusDeleted
	}
	root := buildTree(weights, analyze.SortDisk, TreeOptions{Depth: 2, Top: 3, DeepTop: 1}, "demo", pathStatus, dirStatus)

	if root.Name != "demo" || root.Disk != 722 || root.Size != 2272 || root.Versions != 10 || root.Paths != 7 {
		t.Fatalf("root totals = %+v", root)
	}
	// Largest first, the smallest cut into Hidden.
	if got := names(root); got != "big.bin,src,mid.txt" || root.Hidden != 2 {
		t.Fatalf("level 1 = %s (hidden %d)", got, root.Hidden)
	}
	big, src := root.Children[0], root.Children[1]
	if big.Dir || big.Status != StatusDeleted || big.StatusDisk[splitDeleted] != 500 {
		t.Errorf("big.bin = %+v", big)
	}
	if !src.Dir || src.Disk != 150 || src.Paths != 3 || src.Status != StatusInHead {
		t.Errorf("src = %+v", src)
	}
	// Below the first level DeepTop applies.
	if got := names(src); got != "a.go" || src.Hidden != 2 {
		t.Errorf("src children = %s (hidden %d)", got, src.Hidden)
	}

	// Depth 2 collapses src/deep; sort by size reorders the first level.
	root = buildTree(weights, analyze.SortSize, TreeOptions{Depth: 2}, "demo", pathStatus, dirStatus)
	if got := names(root); got != "mid.txt,big.bin,src,(no path),small.txt" {
		t.Errorf("by size = %s", got)
	}
	for _, c := range root.Children[2].Children {
		if c.Name == "deep" && (!c.Collapsed || len(c.Children) != 0 || c.Disk != 10) {
			t.Errorf("deep = %+v", c)
		}
	}
	// Depth 0 is unlimited.
	root = buildTree(weights, analyze.SortDisk, TreeOptions{}, "demo", pathStatus, dirStatus)
	var files []string
	root.Files(func(n *TreeNode) { files = append(files, n.Path) })
	if len(files) != 7 {
		t.Errorf("unlimited depth files = %v", files)
	}
}

func TestTreeOnRealRepo(t *testing.T) {
	f := buildFixture(t)
	rep, err := Run(Config{Dir: f.dir, Largest: 10, Sort: analyze.SortDisk, By: ByBlob,
		Tree: &TreeOptions{Depth: 2, Top: 8, DeepTop: 5}})
	if err != nil {
		t.Fatal(err)
	}
	tr := rep.Tree
	if tr == nil {
		t.Fatal("no tree")
	}
	var sumDisk, sumSize, sumBlobs int64
	for _, s := range tr.Split {
		sumDisk += s.Disk
		sumSize += s.Size
		sumBlobs += s.Blobs
	}
	if sumDisk != rep.Reachable.BlobDisk || sumSize != rep.Reachable.BlobBytes || sumBlobs != rep.Reachable.Blobs {
		t.Errorf("split %+v does not add up to reachable %+v", tr.Split, rep.Reachable)
	}
	if tr.Root.Disk != rep.Reachable.BlobDisk {
		t.Errorf("root disk %d, reachable %d", tr.Root.Disk, rep.Reachable.BlobDisk)
	}
	if tr.Split[splitDeleted].Blobs != 1 || tr.Split[splitOld].Blobs != 1 {
		t.Errorf("split = %+v", tr.Split)
	}
	byName := map[string]*TreeNode{}
	tr.Root.Files(func(n *TreeNode) { byName[n.Path] = n })
	big := byName["big.bin"]
	if big == nil || big.Status != StatusDeleted || big.Introduced == nil || big.Introduced.Commit != f.c1 {
		t.Errorf("big.bin = %+v", big)
	}
	keep := byName["keep.bin"]
	if keep == nil || keep.Status != StatusInHead || keep.Versions != 2 || keep.StatusBlobs[splitOld] != 1 {
		t.Errorf("keep.bin = %+v", keep)
	}
	// The report's JSON rows are unaffected by the tree.
	if len(rep.Blobs) == 0 || rep.Options.By != ByBlob {
		t.Errorf("rows = %+v", rep.Blobs)
	}
}
