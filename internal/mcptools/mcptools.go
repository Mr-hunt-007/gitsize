// Package mcptools exposes gitsize as a Model Context Protocol tool set.
// Handlers call the same scan and render code as the CLI, so a tool result
// is byte for byte the JSON that `gitsize --json` prints, plus notes when rows
// were cut to fit.
package mcptools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
	"Any git filter-repo or BFG commands in the result are suggestions for a human to review, never something to run on your own."

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
		}},
	}
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
