// Package cli parses flags, runs the scan and maps outcomes to exit codes.
package cli

import (
	"bytes"
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
const Version = "0.3.0"

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
  --svg [FILE]      also write an SVG diagram of where history weight lives:
                    directories and files with every version summed, bars by
                    on-disk size (--sort size: uncompressed), plus a strip
                    splitting it into in HEAD, old versions and deleted.
                    Without FILE (or when the next argument does not end in
                    .svg) it is saved as <repo>-gitsize.svg in the current
                    directory. The normal output still prints
  --svg-depth N     levels drawn in the SVG; 0 = unlimited (default 2)
  --no-color        disable colour (also honours NO_COLOR)
  --mcp             run as an MCP server on stdin/stdout (tools gitsize_report,
                    read-only, and gitsize_svg, writes a new SVG file); other
                    flags are ignored
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
  gitsize --svg                    also save <repo>-gitsize.svg
  gitsize --svg docs/weight.svg --svg-depth 3 ~/src/myapp

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
	svgOut := fs.String("svg", "", "")
	svgDepth := fs.Int("svg-depth", render.SVGDepth, "")

	args = expandOptional(args, "svg", ".svg")

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
	svgDepthSet := false
	fs.Visit(func(f *flag.Flag) { svgDepthSet = svgDepthSet || f.Name == "svg-depth" })
	if svgDepthSet && *svgOut == "" {
		fmt.Fprintln(stderr, "gitsize: --svg-depth only applies with --svg")
		return ExitUsage
	}
	if *svgDepth < 0 {
		fmt.Fprintln(stderr, "gitsize: --svg-depth must be 0 (unlimited) or more")
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

	cfg := scan.Config{Dir: dir, Largest: *largest, Sort: sk, By: mode, History: *history}
	if *svgOut != "" {
		cfg.Tree = &scan.TreeOptions{Depth: *svgDepth, Top: render.SVGTop, DeepTop: render.SVGDeepTop}
	}
	rep, err := scan.Run(cfg)
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

	if *svgOut != "" {
		name := *svgOut
		if name == svgAuto {
			name = render.SVGFileName(rep.Repository.Name)
		}
		if err := writeSVG(name, rep); err != nil {
			fmt.Fprintf(stderr, "gitsize: %v\n", err)
			return ExitError
		}
		fmt.Fprintf(stderr, "gitsize: wrote %s\n", name)
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

// svgAuto marks --svg given without a file name.
const svgAuto = "\x00auto"

// expandOptional lets a string flag be given without a value. "--name" takes
// the next argument as its value only when that argument ends in ext;
// otherwise the flag is set to svgAuto. "--name=value" is always explicit.
func expandOptional(args []string, name, ext string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			return append(out, args[i:]...)
		}
		if a != "-"+name && a != "--"+name {
			out = append(out, a)
			continue
		}
		if i+1 < len(args) && strings.HasSuffix(strings.ToLower(args[i+1]), ext) {
			out = append(out, "--"+name+"="+args[i+1])
			i++
			continue
		}
		out = append(out, "--"+name+"="+svgAuto)
	}
	return out
}

func writeSVG(name string, rep *scan.Report) error {
	var buf bytes.Buffer
	if err := render.SVG(&buf, rep); err != nil {
		return err
	}
	if err := os.WriteFile(name, buf.Bytes(), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", name, err)
	}
	return nil
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
