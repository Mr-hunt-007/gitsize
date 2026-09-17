// Package cli parses flags, runs the scan and maps outcomes to exit codes.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/Mr-hunt-007/gitsize/internal/analyze"
	"github.com/Mr-hunt-007/gitsize/internal/gitx"
	"github.com/Mr-hunt-007/gitsize/internal/mcptools"
	"github.com/Mr-hunt-007/gitsize/internal/render"
	"github.com/Mr-hunt-007/gitsize/internal/scan"
)

// Version is the gitsize release.
const Version = "0.2.0"

// Exit codes.
const (
	ExitOK      = 0
	ExitError   = 1 // git failed or output could not be parsed
	ExitUsage   = 2 // bad flags or arguments
	ExitNotRepo = 3 // the path is not a git repository
	ExitNoGit   = 4 // git is missing or older than 2.31
)

const usage = `gitsize: why is my .git so big?

Usage:
  gitsize [flags] [repo-path]

Reads the repository with the git CLI (never modifies it) and reports what
.git costs on disk, the largest blobs across all history, whether each still
exists in HEAD, and the commit that introduced it.

Flags:
  --largest N       how many rows to show (default 10)
  --sort KEY        rank by "disk" (on-disk, compressed; default) or "size" (uncompressed)
  --by MODE         "blob" (default), "path" (all versions of a path summed),
                    "ext" (by file extension) or "dir" (by containing directory)
  --history         show growth: new blob bytes per month as a bar chart
  --json            print the report as JSON
  --no-color        disable colour (also honours NO_COLOR)
  --mcp             run as an MCP server on stdin/stdout (one read-only tool,
                    gitsize_report); other flags are ignored
  --allow-destructive
                    accepted with --mcp for parity with other tools; gitsize
                    has no destructive tools, so it changes nothing
  --version         print the version
  -h, --help        show this help

Examples:
  gitsize                          scan the repository in the current directory
  gitsize ~/src/myapp --largest 25
  gitsize --by path                which files cost the most across all their versions
  gitsize --by ext --sort size     uncompressed bytes per extension
  gitsize --history                when did the repository grow
  gitsize --json | jq '.largest_blobs[0]'

Exit codes:
  0 success, 1 git error, 2 usage error, 3 not a git repository,
  4 git not found or older than 2.31
`

// Run is the whole program; it returns the process exit code. stdin is only
// read in --mcp mode.
func Run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("gitsize", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	largest := fs.Int("largest", 10, "")
	sortKey := fs.String("sort", "disk", "")
	by := fs.String("by", "blob", "")
	history := fs.Bool("history", false, "")
	asJSON := fs.Bool("json", false, "")
	noColor := fs.Bool("no-color", false, "")
	showVersion := fs.Bool("version", false, "")
	mcpMode := fs.Bool("mcp", false, "")
	allowDestructive := fs.Bool("allow-destructive", false, "")

	var positional []string
	rest := args
	for {
		if err := fs.Parse(rest); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				fmt.Fprint(stdout, usage)
				return ExitOK
			}
			fmt.Fprintf(stderr, "gitsize: %v\nRun 'gitsize --help' for usage.\n", err)
			return ExitUsage
		}
		rest = fs.Args()
		if len(rest) == 0 {
			break
		}
		positional = append(positional, rest[0])
		rest = rest[1:]
	}

	if *showVersion {
		fmt.Fprintf(stdout, "gitsize %s\n", Version)
		return ExitOK
	}
	if *mcpMode {
		return serveMCP(stdin, stdout, stderr, *allowDestructive)
	}
	if *allowDestructive {
		fmt.Fprintln(stderr, "gitsize: --allow-destructive only applies with --mcp")
		return ExitUsage
	}
	if len(positional) > 1 {
		fmt.Fprintf(stderr, "gitsize: expected at most one repository path, got %d\n", len(positional))
		return ExitUsage
	}
	if *largest < 1 {
		fmt.Fprintln(stderr, "gitsize: --largest must be at least 1")
		return ExitUsage
	}
	sk := analyze.SortKey(strings.ToLower(*sortKey))
	if sk != analyze.SortDisk && sk != analyze.SortSize {
		fmt.Fprintf(stderr, "gitsize: --sort must be \"disk\" or \"size\", got %q\n", *sortKey)
		return ExitUsage
	}
	mode := strings.ToLower(*by)
	switch mode {
	case scan.ByBlob, scan.ByPath, scan.ByExt, scan.ByDir:
	default:
		fmt.Fprintf(stderr, "gitsize: --by must be blob, path, ext or dir, got %q\n", *by)
		return ExitUsage
	}
	dir := "."
	if len(positional) == 1 {
		dir = positional[0]
	}

	rep, err := scan.Run(scan.Config{Dir: dir, Largest: *largest, Sort: sk, By: mode, History: *history})
	if err != nil {
		fmt.Fprintf(stderr, "gitsize: %v\n", err)
		switch {
		case errors.Is(err, scan.ErrNotRepo):
			return ExitNotRepo
		case errors.Is(err, gitx.ErrGitNotFound), errors.Is(err, scan.ErrOldGit):
			return ExitNoGit
		}
		return ExitError
	}

	if *asJSON {
		err = render.JSON(stdout, rep)
	} else {
		err = render.Text(stdout, rep, useColor(stdout, *noColor))
	}
	if err != nil {
		fmt.Fprintf(stderr, "gitsize: %v\n", err)
		return ExitError
	}
	return ExitOK
}

// serveMCP runs the MCP server until stdin closes or the process gets
// SIGINT or SIGTERM (clients send SIGTERM on shutdown). A signal cancels
// running scans, which kills their git processes instead of orphaning them.
func serveMCP(stdin io.Reader, stdout, stderr io.Writer, allowDestructive bool) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err := mcptools.New(Version, allowDestructive).Serve(ctx, stdin, stdout)
	if err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintf(stderr, "gitsize: %v\n", err)
		return ExitError
	}
	return ExitOK
}

func useColor(w io.Writer, noColor bool) bool {
	if noColor {
		return false
	}
	if _, set := os.LookupEnv("NO_COLOR"); set {
		return false
	}
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
