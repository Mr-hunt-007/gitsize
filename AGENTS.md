# AGENTS.md

## What this is

`gitsize` is a Go CLI that explains why a repository's `.git` is big: the largest blobs across all history, whether each is still in HEAD, the commit that introduced it, growth per month, suggested (never executed) history-rewrite commands, and an SVG diagram of where history weight lives (`--svg`). It only reads the repository through the git CLI (2.31 or newer). It never rewrites history, runs `gc`, or fetches.

## Layout

- `main.go`: entry point, calls `cli.Run`.
- `internal/cli`: flag parsing, exit codes, help text, `--mcp` startup.
- `internal/scan`: runs git and assembles the `Report` (the `--json` shape lives in `report.go`; the SVG weight tree, not part of the JSON, in `tree.go`).
- `internal/analyze`: pure parsers and aggregation (count-objects, cat-file, ls-tree, raw log, LFS pointers, top-N, fix commands).
- `internal/gitx`: thin boundary around the `git` executable (context-aware, cancelling kills git; on Windows the whole process tree).
- `internal/render`: text, JSON and SVG output (`svg.go`, same layout and CSS as repomap and commitmap with a `gs-` prefix).
- `internal/mcp`: generic stdio MCP server (shared across these tools; do not fork the protocol code).
- `internal/mcptools`: the `gitsize_report` and `gitsize_svg` MCP tools, built on `scan` and `render`.
- `skills/gitsize/SKILL.md`: Agent Skill for using the tool. `docs/gitsize.svg`: README diagram, generated from a demo repository.

## Build and test (as CI runs them)

```sh
gofmt -l .          # must print nothing
go vet ./...
go test -race ./...
go build -o gitsize .
```

CI runs these on ubuntu-latest, macos-latest and windows-latest.

## Rules for contributors

- Go standard library only. No third-party modules. `go 1.22` in `go.mod`.
- Code is gofmt-clean and vet-clean.
- Tests use real fixtures: real git repositories built with `git init` in `t.TempDir()`, skipped with `t.Skip` when git is missing. Isolate from user git config as the existing `TestMain` functions do.
- Tests must pass on Windows: use `filepath`, no `sh`, LF line endings (`.gitattributes`).
- Terminal output in README.md is pasted from real runs of the binary, never typed by hand.
- No em dashes (U+2014) in code, docs or commit messages.
- The `--json` shape is a compatibility contract. Additive changes only; bump `schema` in `internal/scan/report.go` for anything breaking, and document it in README.md.
- Exit codes are documented and stable: 0 success, 1 git error, 2 usage error, 3 not a git repository, 4 git missing or older than 2.31.
- MCP handlers never write to stdout, call `os.Exit`, or `os.Chdir`. `gitsize_report` returns the same JSON as `--json`.
- gitsize must stay read-only on repositories; the only file it writes is the `--svg` diagram. Fix commands are text for a human; nothing in this repo runs them.

## Using gitsize as an agent

- `gitsize --json [repo]` prints one JSON object (shape in README.md, "JSON output"). Check the exit code before parsing.
- `--by blob|path|ext|dir`, `--sort disk|size`, `--largest N`, `--history` shape the report.
- `gitsize --svg [FILE] [--svg-depth N]` also writes a diagram; the path is reported on stderr as `gitsize: wrote FILE`.
- `gitsize --mcp` serves two MCP tools over stdio: `gitsize_report` (read-only, the same JSON) and `gitsize_svg` (writes a new SVG, replaces only an SVG gitsize wrote earlier, returns the absolute path). `--allow-destructive` is accepted but adds nothing: there are no destructive tools.
- Never run the `fix.filter_repo` or `fix.bfg` commands yourself. They rewrite every commit id and need a force-push; show them to the user.
