// Package render turns a scan.Report into text or JSON.
package render

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode"

	"github.com/Mr-hunt-007/gitsize/internal/analyze"
	"github.com/Mr-hunt-007/gitsize/internal/scan"
)

// JSON writes the report as indented JSON.
func JSON(w io.Writer, r *scan.Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(r)
}

type styler struct{ on bool }

func (s styler) wrap(code, text string) string {
	if !s.on {
		return text
	}
	return "\x1b[" + code + "m" + text + "\x1b[0m"
}
func (s styler) bold(t string) string   { return s.wrap("1", t) }
func (s styler) dim(t string) string    { return s.wrap("2", t) }
func (s styler) red(t string) string    { return s.wrap("31", t) }
func (s styler) yellow(t string) string { return s.wrap("33", t) }
func (s styler) green(t string) string  { return s.wrap("32", t) }

var hb = analyze.HumanBytes
var th = analyze.Thousands

// DisplayPath makes a git path safe to print on a terminal: control characters
// (newlines, escape sequences) are shown quoted instead of being emitted raw.
func DisplayPath(p string) string {
	for _, r := range p {
		if unicode.IsControl(r) || r == unicode.ReplacementChar {
			return strconv.Quote(p)
		}
	}
	return p
}

func pad(s string, n int) string {
	w := len([]rune(s))
	if w >= n {
		return s
	}
	return s + strings.Repeat(" ", n-w)
}

func padLeft(s string, n int) string {
	w := len([]rune(s))
	if w >= n {
		return s
	}
	return strings.Repeat(" ", n-w) + s
}

// Text writes the human-readable report.
func Text(w io.Writer, r *scan.Report, color bool) error {
	st := styler{on: color}
	var b strings.Builder
	repo := r.Repository

	kind := ""
	switch {
	case repo.Bare:
		kind = " (bare)"
	}
	fmt.Fprintf(&b, "%s %s%s\n", st.bold("Repository:"), repo.Name, kind)

	parts := []string{"packed " + hb(r.Storage.PackedBytes), "loose " + hb(r.Storage.LooseBytes)}
	if r.Storage.LFSCacheBytes > 0 {
		parts = append(parts, "LFS cache "+hb(r.Storage.LFSCacheBytes))
	}
	parts = append(parts, th(r.Storage.Objects)+" objects")
	gitLabel := ".git"
	if repo.Bare {
		gitLabel = "Git dir"
	}
	fmt.Fprintf(&b, "%s %s  (%s)\n", pad(gitLabel, 10), padLeft(hb(r.Storage.GitDirBytes), 9), strings.Join(parts, ", "))
	if r.HeadTree != nil {
		label := "Checkout"
		if repo.Bare {
			label = "HEAD tree"
		}
		extra := ""
		if r.HeadTree.SizesUnknown > 0 {
			extra = fmt.Sprintf(", %s not present locally and not counted", th(r.HeadTree.SizesUnknown))
		}
		fmt.Fprintf(&b, "%s %s  (files in HEAD: %s%s)\n", pad(label, 10), padLeft(hb(r.HeadTree.Bytes), 9), th(r.HeadTree.Files), extra)
	}
	if !repo.Empty {
		fmt.Fprintf(&b, "%s %s  (%s blob versions in all history, %s uncompressed)\n", pad("Blobs", 10),
			padLeft(hb(r.Reachable.BlobDisk), 9), th(r.Reachable.Blobs), hb(r.Reachable.BlobBytes))
	}

	sortLabel := "on-disk size"
	if r.Options.Sort == string(analyze.SortSize) {
		sortLabel = "uncompressed size"
	}

	switch {
	case repo.Empty:
	case r.Options.By == scan.ByBlob:
		fmt.Fprintf(&b, "\n%s\n", st.bold(fmt.Sprintf("Largest blobs (all history, by %s):", sortLabel)))
		if len(r.Blobs) == 0 {
			b.WriteString("  (none)\n")
			break
		}
		fmt.Fprintf(&b, "  %s\n", st.dim(fmt.Sprintf("%9s  %9s  %-11s  %-18s  %s", "ON DISK", "SIZE", "STATUS", "INTRODUCED", "PATH")))
		for _, e := range r.Blobs {
			suffix := ""
			if e.LFS {
				suffix = st.dim("  (LFS pointer)")
			}
			fmt.Fprintf(&b, "  %9s  %9s  %s  %s  %s%s\n", hb(e.Disk), hb(e.Size),
				statusCell(st, e.Status), introCell(e.Introduced), DisplayPath(e.Path), suffix)
		}
	case r.Options.By == scan.ByPath:
		fmt.Fprintf(&b, "\n%s\n", st.bold(fmt.Sprintf("Largest paths (all versions summed, by %s):", sortLabel)))
		if len(r.Paths) == 0 {
			b.WriteString("  (none)\n")
			break
		}
		fmt.Fprintf(&b, "  %s\n", st.dim(fmt.Sprintf("%9s  %9s  %8s  %-11s  %-18s  %s", "ON DISK", "SIZE", "VERSIONS", "STATUS", "FIRST ADDED", "PATH")))
		for _, e := range r.Paths {
			fmt.Fprintf(&b, "  %9s  %9s  %8s  %s  %s  %s\n", hb(e.Disk), hb(e.Size), th(e.Versions),
				statusCell(st, e.Status), introCell(e.Introduced), DisplayPath(e.Path))
		}
	case r.Options.By == scan.ByExt || r.Options.By == scan.ByDir:
		title, rows, col := "Extensions (all history, by %s):", r.Exts, "EXTENSION"
		if r.Options.By == scan.ByDir {
			title, rows, col = "Directories (all history, by %s; files directly inside each, not in subdirectories):", r.Dirs, "DIRECTORY"
		}
		fmt.Fprintf(&b, "\n%s\n", st.bold(fmt.Sprintf(title, sortLabel)))
		if len(rows) == 0 {
			b.WriteString("  (none)\n")
			break
		}
		fmt.Fprintf(&b, "  %s\n", st.dim(fmt.Sprintf("%9s  %9s  %7s  %s", "ON DISK", "SIZE", "BLOBS", col)))
		for _, g := range rows {
			fmt.Fprintf(&b, "  %9s  %9s  %7s  %s\n", hb(g.Disk), hb(g.Size), th(g.Blobs), DisplayPath(g.Key))
		}
	}

	if r.History != nil {
		writeHistory(&b, st, r)
	}

	if r.Fix != nil {
		writeFix(&b, st, r.Fix)
	}

	if len(r.Notes) > 0 {
		fmt.Fprintf(&b, "\n%s\n", st.bold("Notes:"))
		for _, n := range r.Notes {
			fmt.Fprintf(&b, "  - %s\n", n)
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func statusCell(st styler, s string) string {
	cell := pad(s, 11)
	switch s {
	case scan.StatusDeleted:
		return st.red(cell)
	case scan.StatusOldVersion:
		return st.yellow(cell)
	case scan.StatusInHead:
		return st.green(cell)
	}
	return cell
}

func introCell(in *scan.Introduced) string {
	if in == nil {
		return pad("-", 18)
	}
	short := in.Commit
	if len(short) > 7 {
		short = short[:7]
	}
	return pad(short+" "+in.Date, 18)
}

const barWidth = 40

func writeHistory(b *strings.Builder, st styler, r *scan.Report) {
	h := r.History
	metric := "on disk"
	val := func(m analyze.Month) int64 { return m.Disk }
	if r.Options.Sort == string(analyze.SortSize) {
		metric = "uncompressed"
		val = func(m analyze.Month) int64 { return m.Size }
	}
	fmt.Fprintf(b, "\n%s\n", st.bold(fmt.Sprintf("Growth (new blob bytes %s, by month first committed, UTC):", metric)))
	if len(h.Months) == 0 {
		b.WriteString("  (no commits with file changes)\n")
	}
	var max int64
	for _, m := range h.Months {
		if v := val(m); v > max {
			max = v
		}
	}
	for _, m := range h.Months {
		v := val(m)
		n := 0
		if max > 0 {
			n = int((v*barWidth + max - 1) / max) // round up so any growth is visible
		}
		fmt.Fprintf(b, "  %s  %s  %9s  %s\n", m.Month, pad(strings.Repeat("#", n), barWidth), hb(v), st.dim(plural(m.Blobs, "blob")))
	}
	if h.Unattributed.Blobs > 0 {
		v := h.Unattributed.Disk
		if r.Options.Sort == string(analyze.SortSize) {
			v = h.Unattributed.Size
		}
		fmt.Fprintf(b, "  %s blob(s), %s, are reachable but never appear in a commit diff (for example only via a tag on a tree or blob).\n", th(h.Unattributed.Blobs), hb(v))
	}
}

func writeFix(b *strings.Builder, st styler, f *analyze.Fix) {
	fmt.Fprintf(b, "\n%s\n", st.bold("How to fix:"))
	fmt.Fprintf(b, "  %s, %s on disk, still in history:\n", plural(int64(len(f.Paths)), "deleted path"), hb(f.Bytes))
	for _, p := range f.Paths {
		fmt.Fprintf(b, "    %s\n", DisplayPath(p))
	}
	b.WriteString("  \"Deleted\" means absent from HEAD. Make sure no branch you still use needs them.\n\n")
	b.WriteString("  " + st.yellow("WARNING: both commands below rewrite history.") + " Every commit from the first\n")
	b.WriteString("  one touching these paths gets a new id, on every branch and tag. Back up\n")
	b.WriteString("  first, run it in a fresh clone, force-push, and have every collaborator\n")
	b.WriteString("  re-clone. gitsize never rewrites anything itself.\n\n")
	b.WriteString("  With git-filter-repo (https://github.com/newren/git-filter-repo):\n")
	fmt.Fprintf(b, "    %s\n\n", f.FilterRepo)
	b.WriteString("  Or with BFG Repo-Cleaner (matches by file name in any directory, and\n")
	b.WriteString("  leaves the files in your latest commit untouched):\n")
	for _, c := range f.BFG {
		fmt.Fprintf(b, "    %s\n", c)
	}
	b.WriteString("    git reflog expire --expire=now --all && git gc --prune=now --aggressive\n")
}

func plural(n int64, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return th(n) + " " + word + "s"
}
