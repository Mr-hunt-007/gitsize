// Package mcptools exposes gitsize as a Model Context Protocol tool set.
// Handlers call the same scan and render code as the CLI, so a
// gitsize_report result is byte for byte the JSON that `gitsize --json`
// prints, plus notes when rows were cut to fit, and gitsize_svg writes the
// same file as `gitsize --svg`.
package mcptools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Mr-hunt-007/gitsize/internal/analyze"
	"github.com/Mr-hunt-007/gitsize/internal/mcp"
	"github.com/Mr-hunt-007/gitsize/internal/render"
	"github.com/Mr-hunt-007/gitsize/internal/scan"
)

// Row limits for gitsize_report.
const (
	DefaultLargest = 10
	MaxLargest     = 100
)

const instructions = "gitsize explains why a git repository's .git directory is big. " +
	"Use gitsize_report when a clone, fetch or CI checkout is slow or large, or when asked which files bloat history, whether they still exist in HEAD, which commit added them, or when the repository grew. " +
	"It only reads the repository through the git CLI: it never rewrites history, runs gc or fetches. " +
	"Any git filter-repo or BFG commands in the result are suggestions for a human to review, never something to run on your own. " +
	"gitsize_svg writes a diagram of where the history weight lives, for a README, an issue or a pull request."

const reportDescription = `Explain why a git repository's .git is big. Reads every object reachable from any branch, tag, remote-tracking ref, stash or HEAD and returns the same JSON as ` + "`gitsize --json`" + `.

Use it when a repository is slow to clone or its .git is unexpectedly large, to find the largest files across all of history (including files already deleted from HEAD), who introduced them, and how growth happened over time.

Output (all sizes in bytes, times Unix seconds, dates UTC):
- storage: .git size on disk, packed and loose bytes.
- head_tree: files and bytes in the HEAD commit (what a checkout holds).
- reachable: object and blob totals for all history. "disk" is compressed, after delta; "size" is uncompressed.
- Exactly one list, matching "by": largest_blobs (each blob version: path, oid, size, disk, status, introduced), largest_paths (all versions of a path summed, with versions count), by_ext or by_dir (key, blobs, size, disk).
- status is relative to HEAD: "in HEAD", "old version" (path exists with other content), "deleted" (path absent from HEAD, may still exist on other branches), "unknown" (HEAD does not resolve).
- introduced: earliest commit by committer date whose diff adds the blob or path.
- history (only with history=true): new blob bytes per month first committed.
- fix: present only when deleted paths with at least 1 MiB of history exist. It holds git filter-repo and BFG command strings as TEXT ONLY. This tool never rewrites history and never runs them. Those commands rewrite every commit id on every branch and require a force-push and re-clone: they are suggestions for a human to review, do not run them without explicit user approval.
- notes: caveats (shallow or partial clone, Git LFS, unreachable storage) and, when rows were cut, how many existed and which argument shows more.

Limits: rows are capped by "largest" (default 10, max 100). A blob stored at two paths is listed under one. Scans take time on very large repositories; cancelling the call stops the git processes.`

// New builds the MCP server for gitsize. allowDestructive is accepted for
// parity with the other tools; gitsize has no destructive tools, so it
// changes nothing.
func New(version string, allowDestructive bool) *mcp.Server {
	_ = allowDestructive
	return &mcp.Server{
		Name:         "gitsize",
		Title:        "gitsize",
		Version:      version,
		Instructions: instructions,
		Tools: []mcp.Tool{{
			Name:        "gitsize_report",
			Title:       "Why is .git big",
			Description: reportDescription,
			Annotations: mcp.ReadOnly("Why is .git big"),
			InputSchema: mcp.Object(map[string]any{
				"dir": mcp.String("Repository to scan: any directory inside a work tree, or a bare repository. Absolute, or relative to the server's working directory. Defaults to the server's working directory."),
				"largest": map[string]any{
					"type": "integer", "minimum": 1, "maximum": MaxLargest,
					"description": fmt.Sprintf("How many rows to return for the selected grouping, and how many deleted paths the fix suggestion may cover. Default %d, maximum %d.", DefaultLargest, MaxLargest),
				},
				"sort":    mcp.Enum(`Rank by "disk" (bytes the object occupies in .git, compressed and deltified; default, answers what costs space) or "size" (uncompressed content size).`, "disk", "size"),
				"by":      mcp.Enum(`Grouping: "blob" (default, individual blob versions), "path" (all versions of one path summed, finds files rewritten many times), "ext" (by lower-cased file extension) or "dir" (by the directory directly containing the file, not rolled up).`, scan.ByBlob, scan.ByPath, scan.ByExt, scan.ByDir),
				"history": mcp.Boolean("Also return growth over time: bytes of blobs first committed in each month. Default false."),
			}),
			Handler: report,
		}, svgTool()},
	}
}

func svgTool() mcp.Tool {
	return mcp.Tool{
		Name:  "gitsize_svg",
		Title: "Write a history weight diagram",
		Description: "Writes an SVG diagram of where a git repository's history weight lives (the same file as `gitsize --svg`): " +
			"directories, then files, with every blob version in history summed per path, as a horizontal tree with bars sized by " +
			"on-disk bytes (sort \"size\": uncompressed). It shows the 8 heaviest entries at the first level and 5 below, marks paths deleted " +
			"from HEAD, and adds a strip splitting the weight into in HEAD, old versions and deleted. Hover titles carry exact sizes, " +
			"version counts and introducing commits. The file is standalone, follows light and dark schemes, and renders on GitHub. " +
			"Use it when the user wants a picture of why .git is big for docs, an issue or a pull request; use gitsize_report to read the numbers yourself. " +
			"It only reads the repository. It creates a new file; it replaces an existing file only if that file is an SVG previously written by gitsize, and refuses otherwise. " +
			"Returns the absolute path written, whether a file was replaced, the byte size, and the totals drawn.",
		Annotations: mcp.Writes("Write a history weight diagram"),
		InputSchema: mcp.Object(map[string]any{
			"dir":    mcp.String("Repository to draw: any directory inside a work tree, or a bare repository. Absolute, or relative to the server's working directory. Defaults to the server's working directory."),
			"output": mcp.String("File to write, ending in .svg, absolute or relative to the server's working directory. Its directory must exist. Default: <repo>-gitsize.svg in the working directory, where <repo> is the repository's top-level directory name."),
			"depth": map[string]any{
				"type": "integer", "minimum": 0,
				"description": fmt.Sprintf("Levels drawn below the root; deeper directories are drawn as one bar. 0 means unlimited. Default %d.", render.SVGDepth),
			},
			"sort": mcp.Enum(`Size the bars by "disk" (bytes in .git, compressed and deltified; default) or "size" (uncompressed).`, "disk", "size"),
		}),
		Handler: handleSVG,
	}
}

type svgArgs struct {
	Dir    string `json:"dir"`
	Output string `json:"output"`
	Depth  *int   `json:"depth"`
	Sort   string `json:"sort"`
}

// SVGResult is gitsize_svg's JSON output.
type SVGResult struct {
	Path       string `json:"path"`     // absolute path of the written file
	Replaced   bool   `json:"replaced"` // an earlier gitsize SVG was overwritten
	Bytes      int    `json:"bytes"`
	Repository string `json:"repository"`
	Depth      int    `json:"depth"`
	Sort       string `json:"sort"`
	// Weight drawn: every blob version in history, on disk and uncompressed.
	BlobDiskBytes int64 `json:"blob_disk_bytes"`
	BlobBytes     int64 `json:"blob_bytes"`
	// On-disk bytes by status relative to HEAD, as in the strip.
	InHeadDisk  int64    `json:"in_head_disk_bytes"`
	OldDisk     int64    `json:"old_versions_disk_bytes"`
	DeletedDisk int64    `json:"deleted_disk_bytes"`
	Notes       []string `json:"notes"`
}

func handleSVG(ctx context.Context, raw json.RawMessage) (mcp.Result, error) {
	var a svgArgs
	if err := mcp.Decode(raw, &a); err != nil {
		return mcp.Result{}, err
	}
	depth := render.SVGDepth
	if a.Depth != nil {
		if *a.Depth < 0 {
			return mcp.Result{}, fmt.Errorf("depth must be 0 (unlimited) or more, got %d", *a.Depth)
		}
		depth = *a.Depth
	}
	cfg, err := reportArgs{Dir: a.Dir, Sort: a.Sort}.config()
	if err != nil {
		return mcp.Result{}, err
	}
	// Check an explicit output before spending a scan on it.
	if a.Output != "" {
		if _, _, err := checkOutput(a.Output); err != nil {
			return mcp.Result{}, err
		}
	}
	cfg.Tree = &scan.TreeOptions{Depth: depth, Top: render.SVGTop, DeepTop: render.SVGDeepTop}
	rep, err := scan.RunContext(ctx, cfg)
	if err != nil {
		return mcp.Result{}, err
	}
	out := a.Output
	if out == "" {
		out = render.SVGFileName(rep.Repository.Name)
	}
	absOut, replace, err := checkOutput(out)
	if err != nil {
		return mcp.Result{}, err
	}
	var buf bytes.Buffer
	if err := render.SVG(&buf, rep); err != nil {
		return mcp.Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return mcp.Result{}, err
	}
	if err := writeFile(absOut, buf.Bytes(), replace); err != nil {
		return mcp.Result{}, err
	}
	sp := rep.Tree.Split
	res := SVGResult{
		Path: absOut, Replaced: replace, Bytes: buf.Len(), Repository: rep.Repository.Name,
		Depth: depth, Sort: string(cfg.Sort),
		BlobDiskBytes: rep.Reachable.BlobDisk, BlobBytes: rep.Reachable.BlobBytes,
		InHeadDisk: sp[0].Disk, OldDisk: sp[1].Disk, DeletedDisk: sp[2].Disk,
		Notes: rep.Notes,
	}
	b, err := json.Marshal(res)
	if err != nil {
		return mcp.Result{}, err
	}
	return mcp.Result{Text: string(b), Structured: json.RawMessage(b)}, nil
}

// checkOutput resolves an output path and reports whether an existing file
// there may be replaced: only a regular file that gitsize wrote earlier may be.
func checkOutput(out string) (string, bool, error) {
	if !strings.HasSuffix(strings.ToLower(out), ".svg") {
		return "", false, fmt.Errorf("output must end in .svg, got %q", out)
	}
	abs, err := filepath.Abs(out)
	if err != nil {
		return "", false, err
	}
	if fi, err := os.Stat(filepath.Dir(abs)); err != nil || !fi.IsDir() {
		return "", false, fmt.Errorf("output directory %s does not exist", filepath.Dir(abs))
	}
	fi, err := os.Lstat(abs)
	if errors.Is(err, os.ErrNotExist) {
		return abs, false, nil
	}
	if err != nil {
		return "", false, err
	}
	if !fi.Mode().IsRegular() {
		return "", false, fmt.Errorf("refusing to write %s: it exists and is not a regular file", abs)
	}
	f, err := os.Open(abs)
	if err != nil {
		return "", false, err
	}
	defer f.Close()
	head := make([]byte, 1024)
	n, _ := io.ReadFull(f, head)
	if !bytes.Contains(head[:n], []byte(render.SVGMarker)) {
		return "", false, fmt.Errorf("refusing to overwrite %s: it exists and was not written by gitsize; choose another output", abs)
	}
	return abs, true, nil
}

// writeFile creates path exclusively, or truncates it when replace is set.
func writeFile(path string, data []byte, replace bool) error {
	flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
	if replace {
		flags = os.O_WRONLY | os.O_TRUNC
	}
	f, err := os.OpenFile(path, flags, 0o644)
	if errors.Is(err, os.ErrExist) {
		return fmt.Errorf("refusing to overwrite %s: it was created while the diagram was being built", path)
	}
	if err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

type reportArgs struct {
	Dir     string `json:"dir"`
	Largest *int   `json:"largest"`
	Sort    string `json:"sort"`
	By      string `json:"by"`
	History bool   `json:"history"`
}

func report(ctx context.Context, raw json.RawMessage) (mcp.Result, error) {
	var a reportArgs
	if err := mcp.Decode(raw, &a); err != nil {
		return mcp.Result{}, err
	}
	cfg, err := a.config()
	if err != nil {
		return mcp.Result{}, err
	}
	rep, err := scan.RunContext(ctx, cfg)
	if err != nil {
		return mcp.Result{}, err
	}
	addNotes(rep)
	var buf bytes.Buffer
	if err := render.JSON(&buf, rep); err != nil {
		return mcp.Result{}, err
	}
	out := bytes.TrimRight(buf.Bytes(), "\n")
	return mcp.Result{Text: string(out), Structured: json.RawMessage(out)}, nil
}

func (a reportArgs) config() (scan.Config, error) {
	cfg := scan.Config{Dir: a.Dir, Largest: DefaultLargest, Sort: analyze.SortDisk, By: scan.ByBlob, History: a.History}
	if strings.TrimSpace(cfg.Dir) == "" {
		cfg.Dir = "."
	}
	if a.Largest != nil {
		if *a.Largest < 1 || *a.Largest > MaxLargest {
			return cfg, fmt.Errorf("largest must be between 1 and %d, got %d", MaxLargest, *a.Largest)
		}
		cfg.Largest = *a.Largest
	}
	if a.Sort != "" {
		cfg.Sort = analyze.SortKey(strings.ToLower(a.Sort))
		if cfg.Sort != analyze.SortDisk && cfg.Sort != analyze.SortSize {
			return cfg, fmt.Errorf(`sort must be "disk" or "size", got %q`, a.Sort)
		}
	}
	if a.By != "" {
		cfg.By = strings.ToLower(a.By)
		switch cfg.By {
		case scan.ByBlob, scan.ByPath, scan.ByExt, scan.ByDir:
		default:
			return cfg, fmt.Errorf(`by must be "blob", "path", "ext" or "dir", got %q`, a.By)
		}
	}
	return cfg, nil
}

// addNotes appends truncation and safety notes. Only the notes list changes,
// so the JSON shape stays that of --json.
func addNotes(rep *scan.Report) {
	shown, noun := 0, ""
	switch rep.Options.By {
	case scan.ByBlob:
		shown, noun = len(rep.Blobs), "blob versions"
	case scan.ByPath:
		shown, noun = len(rep.Paths), "paths"
	case scan.ByExt:
		shown, noun = len(rep.Exts), "extensions"
	case scan.ByDir:
		shown, noun = len(rep.Dirs), "directories"
	}
	if rep.Available.Rows > int64(shown) {
		rep.Notes = append(rep.Notes, fmt.Sprintf("Truncated: showing the largest %d of %d %s. %s",
			shown, rep.Available.Rows, noun, raiseHint(rep.Options.Largest)))
	}
	if rep.Fix != nil {
		if rep.Available.FixPaths > len(rep.Fix.Paths) {
			rep.Notes = append(rep.Notes, fmt.Sprintf("Truncated: the fix suggestion covers the largest %d of %d deleted paths with at least 1 MiB of history. %s",
				len(rep.Fix.Paths), rep.Available.FixPaths, raiseHint(rep.Options.Largest)))
		}
		rep.Notes = append(rep.Notes, "The fix commands are suggestions only and were not run. They rewrite history on every branch and tag and need a force-push and a re-clone by every collaborator, so show them to the user to review instead of running them.")
	}
}

func raiseHint(largest int) string {
	if largest < MaxLargest {
		return fmt.Sprintf("Raise the largest argument (up to %d) to see more.", MaxLargest)
	}
	return fmt.Sprintf("%d is the maximum here; run gitsize --largest N --json from a shell for more.", MaxLargest)
}
