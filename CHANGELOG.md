# Changelog

## 0.3.0 (2026-09-17)

- `--svg [FILE]` also writes an SVG diagram of where the history weight lives: directories, then files, with every blob version in history summed, bars by on-disk size (`--sort size`: uncompressed), paths deleted from HEAD marked, and a strip splitting the weight into in HEAD, old versions and deleted. Without a file name it is saved as `<repo>-gitsize.svg`. Light and dark schemes, no external references.
- `--svg-depth N` sets the levels drawn (default 2, 0 for unlimited).
- MCP: a second tool, `gitsize_svg`, writes the diagram and returns its absolute path. It never overwrites a file gitsize did not write.
- On Windows, cancelling a scan now kills the whole git process tree (Git for Windows' `git.exe` on PATH is a launcher for the real git).

## 0.2.0 (2026-09-17)

- `--mcp` runs an MCP server over stdio with one read-only tool, `gitsize_report`, returning the same JSON as `--json`. Rows are capped (`largest`, default 10, max 100) and `notes` say when something was cut. Fix commands are returned as text only, with a note that they are for a human to review.
- Cancelling an MCP call, or stopping the server with SIGINT or SIGTERM, kills the running git processes.
- `--allow-destructive` is accepted with `--mcp` for consistency; there are no destructive tools.
- `AGENTS.md`, `CLAUDE.md`, `llms.txt` and an Agent Skill in `skills/gitsize/`.

## 0.1.0 (2026-09-17)

First release.

- `.git` size breakdown from `git count-objects -v`, plus the size of the HEAD tree.
- Largest blobs across all refs from one streaming `git rev-list --objects --all` into one `git cat-file --batch-check`, with on-disk and uncompressed sizes (`--sort disk|size`).
- Per-blob status relative to HEAD (in HEAD, old version, deleted) and the introducing commit, found in a single `git log --all --raw` pass.
- `--by path`, `--by ext`, `--by dir` aggregations and `--largest N`.
- `--history` growth chart of new blob bytes per month.
- "How to fix" section with `git filter-repo` and BFG commands, only when large deleted paths exist. Nothing is ever rewritten.
- Handles empty, bare, shallow and partial-clone repositories, Git LFS pointers, and paths with spaces, unicode and (with git 2.50+) newlines.
- `--json`, `--no-color` / `NO_COLOR`, documented exit codes.
