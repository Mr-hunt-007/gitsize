package render

import (
	"fmt"
	"html"
	"io"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/Mr-hunt-007/gitsize/internal/analyze"
	"github.com/Mr-hunt-007/gitsize/internal/scan"
)

// Layout constants for the SVG diagram. Text uses a monospace stack at 12px
// so label widths can be estimated without font metrics. They match the
// diagrams of repomap and commitmap.
const (
	svgFont     = 12.0
	svgCharW    = 7.3 // advance of a 12px monospace glyph, rounded up
	svgRowH     = 32.0
	svgBarMax   = 160.0
	svgBarH     = 8.0
	svgColGap   = 56.0
	svgMargin   = 24.0
	svgHeaderH  = 64.0
	svgNameMax  = 40 // display cells before a label is shortened
	svgMinWidth = 520.0
)

// SVG tree limits used by the CLI and the MCP tool.
const (
	SVGDepth   = 2
	SVGTop     = 8 // entries at the first level
	SVGDeepTop = 5 // entries per directory below the first level
)

// SVGMarker identifies an SVG written by gitsize.
const SVGMarker = `class="gs-svg"`

var svgPalette = []string{"#4e79a7", "#f28e2b", "#e15759", "#59a14f", "#b07aa1", "#76b7b2", "#edc948", "#9c755f", "#ff9da7", "#8cd17d"}

const svgGrey = "#8c959f"

// svgItem is a tree node or a "+N more" marker placed in the diagram.
type svgItem struct {
	node   *scan.TreeNode // nil for a "+N more" marker
	name   string
	more   int
	depth  int
	y      float64
	parent *svgItem
	color  string
}

// SVG writes a standalone SVG diagram of where the repository's history
// weight lives: a horizontal tree of directories and files with every blob
// version summed, and a strip splitting the weight into in HEAD, old
// versions and deleted. The report must have been scanned with Config.Tree.
func SVG(w io.Writer, r *scan.Report) error {
	if r.Tree == nil {
		return fmt.Errorf("the scan did not build the weight tree")
	}
	_, err := io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?>`+"\n"+svgBody(r))
	return err
}

// SVGFileName is the default --svg output name for a repository name.
func SVGFileName(repoName string) string {
	name := strings.Map(func(r rune) rune {
		if r < 0x20 || strings.ContainsRune(`<>:"/\|?*`, r) {
			return '_'
		}
		return r
	}, repoName)
	if name == "" || name == "." || name == ".." || name == string(filepath.Separator) {
		name = "repository"
	}
	return name + "-gitsize.svg"
}

func sortKey(r *scan.Report) analyze.SortKey {
	if r.Options.Sort == string(analyze.SortSize) {
		return analyze.SortSize
	}
	return analyze.SortDisk
}

// label is the display name of a node: directories end in "/".
func (it *svgItem) label() string {
	if it.more > 0 {
		return fmt.Sprintf("+%d more", it.more)
	}
	return it.name
}

func svgBody(r *scan.Report) string {
	k := sortKey(r)
	title := svgEllipsize(svgSafe(r.Repository.Name), svgNameMax)
	root := r.Tree.Root

	var items []*svgItem
	row := 0
	var place func(n *scan.TreeNode, depth int, parent *svgItem, color string) *svgItem
	place = func(n *scan.TreeNode, depth int, parent *svgItem, color string) *svgItem {
		it := &svgItem{node: n, depth: depth, parent: parent, color: color}
		switch {
		case depth == 0:
			it.name = title
		case n.Dir:
			it.name = svgEllipsize(svgSafe(n.Name), svgNameMax-1) + "/"
		default:
			it.name = svgEllipsize(svgSafe(n.Name), svgNameMax)
		}
		items = append(items, it)
		if len(n.Children) == 0 && n.Hidden == 0 {
			it.y = float64(row)
			row++
			return it
		}
		first, last := -1.0, -1.0
		for i, c := range n.Children {
			cc := color
			if depth == 0 {
				cc = svgPalette[i%len(svgPalette)]
			}
			ci := place(c, depth+1, it, cc)
			if first < 0 {
				first = ci.y
			}
			last = ci.y
		}
		if n.Hidden > 0 {
			m := &svgItem{more: n.Hidden, depth: depth + 1, parent: it, y: float64(row), color: color}
			row++
			items = append(items, m)
			if first < 0 {
				first = m.y
			}
			last = m.y
		}
		it.y = (first + last) / 2
		return it
	}
	place(root, 0, nil, "")

	var maxVal int64 = 1
	maxDepth := 0
	for _, it := range items {
		if it.node != nil && it.depth > 0 {
			maxVal = max(maxVal, it.node.Value(k))
		}
		maxDepth = max(maxDepth, it.depth)
	}
	colW := make([]float64, maxDepth+1)
	for _, it := range items {
		// One spare cell: some monospace fonts (SF Mono) advance slightly
		// more than svgCharW.
		cells := utf8.RuneCountInString(it.label()) + 1
		if it.node != nil {
			cells += 1 + utf8.RuneCountInString(hb(it.node.Value(k)))
			if svgDeleted(it) {
				cells += 1 + len("deleted")
			}
		}
		colW[it.depth] = max(colW[it.depth], float64(cells)*svgCharW)
	}
	colX := make([]float64, maxDepth+1)
	x := svgMargin
	for d := range colW {
		colW[d] = max(colW[d], svgBarMax)
		colX[d] = x
		x += colW[d] + svgColGap
	}
	width := x - svgColGap + svgMargin

	sub := svgSubtitle(r)
	meta := "bars by on-disk size"
	if k == analyze.SortSize {
		meta = "bars by uncompressed size"
	}
	if r.Tree.Depth > 0 {
		meta += fmt.Sprintf(", depth %d", r.Tree.Depth)
	}
	width = max(width, svgMargin*2+float64(utf8.RuneCountInString(title))*svgCharW*1.25)
	width = max(width, svgMargin*2+float64(utf8.RuneCountInString(sub+"  "+meta))*svgCharW)
	width = max(width, svgMinWidth)

	split := svgSplit(r.Tree, k)
	legendCols := max(1, int((width-2*svgMargin)/(28*svgCharW+24)))
	legendRows := (len(split) + legendCols - 1) / legendCols
	treeH := float64(max(row, 1)) * svgRowH
	stripTop := svgHeaderH + treeH + 12
	height := stripTop + svgMargin
	if len(split) > 0 {
		height = stripTop + 28 + 14 + float64(legendRows)*22 + svgMargin
	}

	yOf := func(it *svgItem) float64 { return svgHeaderH + it.y*svgRowH + svgRowH/2 }
	barW := func(it *svgItem) float64 {
		if it.depth == 0 {
			return svgBarMax
		}
		return max(3, svgBarMax*float64(it.node.Value(k))/float64(maxVal))
	}

	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" class="gs-svg" width="%.0f" height="%.0f" viewBox="0 0 %.0f %.0f" role="img" aria-label="%s">`+"\n",
		width, height, width, height, svgEsc("history weight of "+title))
	b.WriteString(svgStyle)
	fmt.Fprintf(&b, `<rect class="gs-bg" x="0" y="0" width="%.0f" height="%.0f"/>`+"\n", width, height)
	fmt.Fprintf(&b, `<text class="gs-fg gs-title" x="%.0f" y="30">%s</text>`+"\n", svgMargin, svgEsc(title))
	fmt.Fprintf(&b, `<text class="gs-muted" x="%.0f" y="50">%s  <tspan class="gs-faint">%s</tspan></text>`+"\n", svgMargin, svgEsc(sub), svgEsc(meta))

	// links first so bars and labels paint over them
	b.WriteString(`<g class="gs-links">` + "\n")
	for _, it := range items {
		if it.parent == nil {
			continue
		}
		p := it.parent
		py := yOf(p) + 1 + svgBarH/2
		cy := yOf(it) + 1 + svgBarH/2
		sx := colX[p.depth] + barW(p)
		ex := colX[it.depth] - 6
		mx := colX[p.depth] + colW[p.depth] + 8
		cls := "gs-link"
		if it.more > 0 {
			cls = "gs-link gs-dashed"
		}
		fmt.Fprintf(&b, `<path class="%s" d="M%.1f %.1f H%.1f C%.1f %.1f %.1f %.1f %.1f %.1f"/>`+"\n",
			cls, sx, py, mx, (mx+ex)/2, py, (mx+ex)/2, cy, ex, cy)
	}
	b.WriteString("</g>\n")

	clipID := 0 // unique ids for the rounded clip of each stacked bar
	for _, it := range items {
		x, y := colX[it.depth], yOf(it)
		if it.more > 0 {
			fmt.Fprintf(&b, `<text class="gs-muted gs-more" x="%.1f" y="%.1f">%s</text>`+"\n", x, y+svgFont/2-1, svgEsc(it.label()))
			continue
		}
		n := it.node
		b.WriteString("<g>")
		fmt.Fprintf(&b, `<title>%s</title>`, svgEsc(svgTip(r, it)))
		deleted := svgDeleted(it)
		nameCls := "gs-fg"
		if deleted {
			nameCls = "gs-muted"
		}
		if it.depth <= 1 {
			nameCls += " gs-strong"
		}
		fmt.Fprintf(&b, `<text x="%.1f" y="%.1f"><tspan class="%s">%s</tspan><tspan class="gs-muted" dx="%.1f">%s</tspan>`,
			x, y-3, nameCls, svgEsc(it.name), svgCharW, svgEsc(hb(n.Value(k))))
		if deleted {
			fmt.Fprintf(&b, `<tspan class="gs-faint gs-more" dx="%.1f">deleted</tspan>`, svgCharW)
		}
		b.WriteString("</text>")
		if it.depth == 0 {
			fmt.Fprintf(&b, `<rect class="gs-rootbar" x="%.1f" y="%.1f" width="%.1f" height="%.0f" rx="2"/>`, x, y+1, barW(it), svgBarH)
		} else {
			svgStackedBar(&b, x, y+1, barW(it), n, clipID)
			clipID++
		}
		b.WriteString("</g>\n")
	}

	if len(split) > 0 {
		fmt.Fprintf(&b, `<line class="gs-rule" x1="%.0f" y1="%.1f" x2="%.1f" y2="%.1f"/>`+"\n", svgMargin, stripTop, width-svgMargin, stripTop)
		fmt.Fprintf(&b, `<text class="gs-fg gs-strong" x="%.0f" y="%.1f">history weight</text>`+"\n", svgMargin, stripTop+24)
		stripY := stripTop + 34
		stripW := width - 2*svgMargin
		sx := svgMargin
		for i, s := range split {
			w := stripW * s.Share
			if i == len(split)-1 {
				w = svgMargin + stripW - sx // absorb rounding so the strip ends flush
			}
			if w <= 0 {
				continue
			}
			fmt.Fprintf(&b, `<rect x="%.1f" y="%.1f" width="%.1f" height="%.0f" fill="%s"><title>%s</title></rect>`+"\n",
				sx, stripY, w, svgBarH, s.Color, svgEsc(svgSplitTip(s)))
			sx += w
		}
		colWidth := stripW / float64(legendCols)
		for i, s := range split {
			lx := svgMargin + float64(i%legendCols)*colWidth
			ly := stripY + svgBarH + 26 + float64(i/legendCols)*22
			fmt.Fprintf(&b, `<g><title>%s</title>`, svgEsc(svgSplitTip(s)))
			fmt.Fprintf(&b, `<rect x="%.1f" y="%.1f" width="10" height="10" rx="2" fill="%s"/>`, lx, ly-9, s.Color)
			fmt.Fprintf(&b, `<text x="%.1f" y="%.1f"><tspan class="gs-fg">%s</tspan><tspan class="gs-muted" dx="%.1f">%s</tspan><tspan class="gs-muted" dx="%.1f">%s</tspan></text></g>`+"\n",
				lx+16, ly, svgEsc(s.Label), svgCharW, svgEsc(hb(s.Bytes)), svgCharW, svgPercent(s.Share))
		}
	}
	b.WriteString("</svg>\n")
	return b.String()
}

// svgDeleted reports whether a node is gone from HEAD: a file path that HEAD
// does not have, or a directory with nothing under it in HEAD.
func svgDeleted(it *svgItem) bool {
	return it.node != nil && it.depth > 0 && it.node.Status == scan.StatusDeleted
}

func svgSubtitle(r *scan.Report) string {
	git := ".git"
	if r.Repository.Bare {
		git = "git dir"
	}
	parts := []string{hb(r.Storage.GitDirBytes) + " " + git, plural(r.Storage.Objects, "object")}
	if r.HeadTree != nil {
		parts = append(parts, hb(r.HeadTree.Bytes)+" in HEAD")
	}
	return strings.Join(parts, ", ")
}

func svgTip(r *scan.Report, it *svgItem) string {
	n := it.node
	var b strings.Builder
	if it.depth == 0 {
		b.WriteString(svgSafe(r.Repository.Name))
	} else {
		b.WriteString(DisplayPath(n.Path))
		if n.Dir {
			b.WriteString("/")
		}
	}
	fmt.Fprintf(&b, ": %s on disk, %s uncompressed", hb(n.Disk), hb(n.Size))
	if !n.Dir {
		switch n.Status {
		case scan.StatusInHead:
			b.WriteString("; in HEAD")
		case scan.StatusDeleted:
			b.WriteString("; deleted (not in HEAD)")
		default:
			b.WriteString("; status unknown (HEAD does not resolve)")
		}
		fmt.Fprintf(&b, "; %s", plural(n.Versions, "version"))
	} else {
		fmt.Fprintf(&b, "; %s of %s", plural(n.Versions, "blob version"), plural(n.Paths, "file"))
		if it.depth > 0 && n.Status == scan.StatusDeleted {
			b.WriteString("; deleted (nothing under it in HEAD)")
		}
		if n.Collapsed {
			b.WriteString("; contents not drawn at this depth")
		}
	}
	// Where the bytes sit, when it is not all one status.
	var split []string
	for i, label := range []string{"in HEAD", "old versions", "deleted", "unknown"} {
		if n.StatusDisk[i] > 0 && n.StatusBlobs[i] < n.Versions {
			split = append(split, fmt.Sprintf("%s %s", label, hb(n.StatusDisk[i])))
		}
	}
	if len(split) > 0 {
		b.WriteString(" (on disk: " + strings.Join(split, ", ") + ")")
	}
	if n.Introduced != nil {
		short := n.Introduced.Commit
		if len(short) > 7 {
			short = short[:7]
		}
		fmt.Fprintf(&b, "; introduced in %s on %s", short, n.Introduced.Date)
	}
	return b.String()
}

// svgSplitEntry is one slice of the history weight strip.
type svgSplitEntry struct {
	Label string
	Color string
	Blobs int64
	Bytes int64
	Other int64 // the other size measure, for the hover title
	Share float64
	Size  bool // Bytes are uncompressed
}

// svgStatusColors colour history weight by blob status, in the order of
// TreeNode.StatusDisk: in HEAD, old versions, deleted, unknown. The tree bars
// and the strip use the same colours, so a red bar segment always means
// bytes from deleted files.
var svgStatusColors = [4]string{svgPalette[0], svgPalette[1], svgPalette[2], svgGrey}

// svgStackedBar draws a node's bar split by blob status. The split uses
// on-disk bytes, the only per-status measure recorded; with --sort size the
// bar length is uncompressed and the split keeps the on-disk proportions.
func svgStackedBar(b *strings.Builder, x, y, w float64, n *scan.TreeNode, id int) {
	var total int64
	for _, d := range n.StatusDisk {
		total += d
	}
	if total == 0 {
		fmt.Fprintf(b, `<rect x="%.1f" y="%.1f" width="%.1f" height="%.0f" rx="2" fill="%s"/>`, x, y, w, svgBarH, svgGrey)
		return
	}
	fmt.Fprintf(b, `<clipPath id="gs-bar-%d"><rect x="%.1f" y="%.1f" width="%.1f" height="%.0f" rx="2"/></clipPath><g clip-path="url(#gs-bar-%d)">`, id, x, y, w, svgBarH, id)
	sx := x
	for i, d := range n.StatusDisk {
		if d == 0 {
			continue
		}
		sw := w * float64(d) / float64(total)
		fmt.Fprintf(b, `<rect x="%.1f" y="%.1f" width="%.1f" height="%.0f" fill="%s"/>`, sx, y, sw, svgBarH, svgStatusColors[i])
		sx += sw
	}
	b.WriteString("</g>")
}

// svgSplit returns the non-empty slices of history weight by status, in the
// order in HEAD, old versions, deleted, unknown. Shares sum to 1. Empty
// slices are left out instead of showing as 0.0%.
func svgSplit(t *scan.Tree, k analyze.SortKey) []svgSplitEntry {
	labels := []string{"in HEAD", "old versions", "deleted", "unknown"}
	colors := svgStatusColors
	var total int64
	for _, s := range t.Split {
		if k == analyze.SortSize {
			total += s.Size
		} else {
			total += s.Disk
		}
	}
	if total == 0 {
		return nil
	}
	var out []svgSplitEntry
	for i, s := range t.Split {
		e := svgSplitEntry{Label: labels[i], Color: colors[i], Blobs: s.Blobs, Bytes: s.Disk, Other: s.Size, Size: k == analyze.SortSize}
		if e.Size {
			e.Bytes, e.Other = s.Size, s.Disk
		}
		if e.Bytes == 0 {
			continue
		}
		e.Share = float64(e.Bytes) / float64(total)
		out = append(out, e)
	}
	return out
}

func svgSplitTip(s svgSplitEntry) string {
	if s.Size {
		return fmt.Sprintf("%s: %s uncompressed (%s bytes), %s on disk, %s, %s",
			s.Label, hb(s.Bytes), th(s.Bytes), hb(s.Other), plural(s.Blobs, "blob version"), svgPercent(s.Share))
	}
	return fmt.Sprintf("%s: %s on disk (%s bytes), %s uncompressed, %s, %s",
		s.Label, hb(s.Bytes), th(s.Bytes), hb(s.Other), plural(s.Blobs, "blob version"), svgPercent(s.Share))
}

// svgPercent never shows a visible slice as "0.0%".
func svgPercent(f float64) string {
	if f > 0 && f < 0.0005 {
		return "<0.1%"
	}
	return fmt.Sprintf("%.1f%%", f*100)
}

// svgSafe quotes names containing control characters so a hostile path
// cannot break the layout.
func svgSafe(s string) string {
	if d := DisplayPath(s); d != s {
		return d[1 : len(d)-1]
	}
	return s
}

func svgEllipsize(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

func svgEsc(s string) string { return html.EscapeString(s) }

const svgStyle = `<style>
.gs-svg text{font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,"Liberation Mono",monospace;font-size:12px}
.gs-svg .gs-title{font-size:15px;font-weight:600}
.gs-svg .gs-bg{fill:#ffffff}
.gs-svg .gs-fg{fill:#1f2328}
.gs-svg .gs-strong{font-weight:600}
.gs-svg .gs-muted{fill:#59636e}
.gs-svg .gs-faint{fill:#818b98}
.gs-svg .gs-more{font-style:italic}
.gs-svg .gs-link{fill:none;stroke:#c8d1da;stroke-width:1.25}
.gs-svg .gs-dashed{stroke-dasharray:3 3}
.gs-svg .gs-rule{stroke:#d1d9e0;stroke-width:1}
.gs-svg .gs-rootbar{fill:#57606a}
@media (prefers-color-scheme: dark){
.gs-svg .gs-bg{fill:#0d1117}
.gs-svg .gs-fg{fill:#e6edf3}
.gs-svg .gs-muted{fill:#9198a1}
.gs-svg .gs-faint{fill:#6e7681}
.gs-svg .gs-link{stroke:#3d444d}
.gs-svg .gs-rule{stroke:#3d444d}
.gs-svg .gs-rootbar{fill:#9198a1}
}
</style>
`
