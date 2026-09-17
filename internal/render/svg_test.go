package render

import (
	"bytes"
	"encoding/xml"
	"io"
	"regexp"
	"strings"
	"testing"

	"github.com/Mr-hunt-007/gitsize/internal/analyze"
	"github.com/Mr-hunt-007/gitsize/internal/scan"
)

func svgSample() *scan.Report {
	file := func(name, path string, disk int64, status string, statusIdx int) *scan.TreeNode {
		n := &scan.TreeNode{Name: name, Path: path, Paths: 1, Versions: 2, Size: disk * 2, Disk: disk, Status: status}
		n.StatusDisk[statusIdx] = disk
		n.StatusBlobs[statusIdx] = 2
		return n
	}
	rd := &scan.TreeNode{Name: "r&d", Path: "r&d", Dir: true, Paths: 2, Versions: 4, Size: 600, Disk: 300, Status: scan.StatusInHead,
		Children: []*scan.TreeNode{file("notes & plans.txt", "r&d/notes & plans.txt", 200, scan.StatusInHead, 0), file("old.txt", "r&d/old.txt", 100, scan.StatusDeleted, 2)}}
	dump := file("dump.sql", "dump.sql", 5000, scan.StatusDeleted, 2)
	dump.Introduced = &scan.Introduced{Commit: "0123456789abcdef", Date: "2024-01-10"}
	root := &scan.TreeNode{Name: "demo", Dir: true, Paths: 3, Versions: 6, Size: 10600, Disk: 5300, Status: scan.StatusInHead,
		Children: []*scan.TreeNode{dump, rd}, Hidden: 2}
	return &scan.Report{
		Repository: scan.Repository{Name: `demo & "co"`},
		Storage:    scan.Storage{GitDirBytes: 486 << 20, Objects: 12408},
		HeadTree:   &scan.HeadTree{Bytes: 15 << 20},
		Options:    scan.Options{Sort: "disk"},
		Tree: &scan.Tree{Root: root, Depth: 2, Split: [4]scan.SplitEntry{
			{Status: scan.StatusInHead, Blobs: 2, Size: 400, Disk: 200},
			{Status: scan.StatusOldVersion},
			{Status: scan.StatusDeleted, Blobs: 4, Size: 10200, Disk: 5100},
			{Status: scan.StatusUnknown},
		}},
	}
}

func renderSVG(t *testing.T, r *scan.Report) string {
	t.Helper()
	var b bytes.Buffer
	if err := SVG(&b, r); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestSVGWellFormedAndEscaped(t *testing.T) {
	out := renderSVG(t, svgSample())
	d := xml.NewDecoder(strings.NewReader(out))
	for {
		_, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("SVG is not well-formed XML: %v\n%s", err, out)
		}
	}
	for _, want := range []string{
		"demo &amp; &#34;co&#34;", "r&amp;d/", "notes &amp; plans.txt",
		"486 MiB .git, 12,408 objects, 15 MiB in HEAD", "bars by on-disk size, depth 2",
		"history weight", "prefers-color-scheme: dark", SVGMarker, "+2 more",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("SVG missing %q in\n%s", want, out)
		}
	}
	for _, want := range []string{
		"dump.sql: 4.9 KiB on disk, 9.8 KiB uncompressed; deleted (not in HEAD); 2 versions; introduced in 0123456 on 2024-01-10",
		"r&amp;d/notes &amp; plans.txt: 200 B on disk, 400 B uncompressed; in HEAD; 2 versions",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("hover title missing %q", want)
		}
	}
	// Deleted paths: muted label plus a "deleted" suffix; in-HEAD ones are not.
	if !strings.Contains(out, `<tspan class="gs-muted gs-strong">dump.sql</tspan>`) || strings.Count(out, `<tspan class="gs-faint gs-more" dx="7.3">deleted</tspan>`) != 2 {
		t.Errorf("deleted marking wrong:\n%s", out)
	}
	if !strings.Contains(out, `<tspan class="gs-fg">notes &amp; plans.txt</tspan>`) {
		t.Error("in-HEAD file should use the normal label colour")
	}
	// Order: heaviest first, then the "+N more" marker.
	if i, j, k := strings.Index(out, ">dump.sql<"), strings.Index(out, ">r&amp;d/<"), strings.Index(out, "+2 more"); !(i < j && j < k) {
		t.Errorf("order dump=%d r&d=%d more=%d", i, j, k)
	}
}

func TestSVGSplitSumsToWhole(t *testing.T) {
	tr := &scan.Tree{Split: [4]scan.SplitEntry{
		{Disk: 700, Size: 100}, {Disk: 299, Size: 100}, {Disk: 1, Size: 0}, {},
	}}
	got := svgSplit(tr, analyze.SortDisk)
	if len(got) != 3 || got[0].Label != "in HEAD" || got[1].Label != "old versions" || got[2].Label != "deleted" {
		t.Fatalf("got %+v", got)
	}
	sum := 0.0
	for _, s := range got {
		sum += s.Share
	}
	if sum < 0.999999 || sum > 1.000001 {
		t.Errorf("shares sum to %v", sum)
	}
	if got[0].Color != svgPalette[0] || got[1].Color != svgPalette[1] || got[2].Color != svgPalette[2] {
		t.Error("strip colours must be the first three palette colours")
	}
	if svgPercent(got[2].Share) != "0.1%" || svgPercent(0.0001) != "<0.1%" {
		t.Errorf("percent: %s", svgPercent(got[2].Share))
	}
	// --sort size: empty uncompressed slices are left out, not shown as 0.0%.
	got = svgSplit(tr, analyze.SortSize)
	if len(got) != 2 || got[0].Share != 0.5 {
		t.Errorf("size split = %+v", got)
	}
	if svgSplit(&scan.Tree{}, analyze.SortDisk) != nil {
		t.Error("no blobs should mean no strip")
	}
	// The drawn strip segments end flush with the strip.
	out := renderSVG(t, svgSample())
	if n := len(regexp.MustCompile(`height="8" fill="#[0-9a-f]+"><title>`).FindAllString(out, -1)); n != 2 {
		t.Errorf("want 2 strip segments")
	}
}

func TestSVGFileName(t *testing.T) {
	for in, want := range map[string]string{
		"myapp":   "myapp-gitsize.svg",
		"a:b":     "a_b-gitsize.svg",
		"":        "repository-gitsize.svg",
		"r&d app": "r&d app-gitsize.svg",
	} {
		if got := SVGFileName(in); got != want {
			t.Errorf("SVGFileName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSVGWithoutTree(t *testing.T) {
	if err := SVG(io.Discard, &scan.Report{}); err == nil {
		t.Error("expected an error without a tree")
	}
}

func TestSVGBarsSplitByStatus(t *testing.T) {
	n := &scan.TreeNode{Name: "data", Dir: true, StatusDisk: [4]int64{25, 0, 75, 0}}
	var b strings.Builder
	svgStackedBar(&b, 10, 20, 100, n, 7)
	out := b.String()
	for _, want := range []string{
		`<clipPath id="gs-bar-7">`,
		`clip-path="url(#gs-bar-7)"`,
		`width="25.0" height="8" fill="` + svgStatusColors[0] + `"`,                   // in HEAD
		`x="35.0" y="20.0" width="75.0" height="8" fill="` + svgStatusColors[2] + `"`, // deleted, after it
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %s", want, out)
		}
	}
	if strings.Contains(out, svgStatusColors[1]) {
		t.Error("empty status should draw no segment")
	}
	// Bars and strip share one colour meaning.
	if svgStatusColors[2] != svgPalette[2] {
		t.Error("deleted must be the strip's deleted colour")
	}
	// Every clip id in a full render is unique.
	out = renderSVG(t, svgSample())
	ids := regexp.MustCompile(`<clipPath id="(gs-bar-\d+)">`).FindAllStringSubmatch(out, -1)
	seen := map[string]bool{}
	for _, m := range ids {
		if seen[m[1]] {
			t.Errorf("duplicate clip id %s", m[1])
		}
		seen[m[1]] = true
	}
	if len(ids) == 0 {
		t.Error("full render has no stacked bars")
	}
}
