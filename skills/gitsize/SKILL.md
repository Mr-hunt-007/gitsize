---
name: gitsize
description: Explain why a git repository's .git directory is big, finding the largest files in all history, whether they are still in HEAD, and the commit that added them, or draw it as an SVG diagram. Use when a repo is slow to clone, fetch or check out in CI, when .git is unexpectedly large, or before removing big files from history.
---

# gitsize

Read-only report of what makes a repository's `.git` big. It shells out to git (2.31 or newer), never rewrites history, never runs `gc`, never fetches.

## When to use

- "Why is this repo so big / slow to clone?"
- Finding a large file that was committed and later deleted (still in history).
- Finding files that are small but rewritten many times (`--by path`).
- Seeing when the repository grew (`--history`).
- Before suggesting `git filter-repo` or BFG, to know exactly which paths matter.

## Commands

```sh
gitsize --json                         # current repo, top 10 blob versions by on-disk size
gitsize --json --largest 25 path/to/repo
gitsize --json --by path               # all versions of each path summed
gitsize --json --by ext                # per extension; --by dir per directory
gitsize --json --sort size             # rank by uncompressed size instead of on-disk
gitsize --json --history               # adds bytes first committed per month
gitsize --json --svg docs/weight.svg   # also write a diagram (--svg-depth N, default 2)
```

If the `gitsize` MCP server is configured, call `gitsize_report` with `dir`, `largest` (1 to 100), `sort`, `by` and `history` instead; it returns the same JSON. `gitsize_svg` (`dir`, `output`, `depth`, `sort`) writes the diagram and returns its absolute path.

## Reading the output

- `storage.git_dir_bytes`: what `.git` costs on disk. `head_tree.bytes`: what a checkout holds. A big gap means history, not current files, is the problem.
- `reachable.blob_disk_bytes`: all blob storage across history.
- `largest_blobs[]` (or `largest_paths[]`, `by_ext[]`, `by_dir[]`, matching `options.by`). `disk` is compressed bytes in `.git` (what costs space); `size` is uncompressed.
- `status`: `in HEAD`, `old version` (path exists with different content), `deleted` (absent from HEAD, but possibly present on another branch), `unknown`.
- `introduced.commit` / `introduced.date`: earliest commit adding that blob or path.
- `fix`: only present when deleted paths hold at least 1 MiB. `fix.filter_repo` and `fix.bfg` are command strings.
- `notes`: read them. They flag shallow clones and partial clones (totals are partial), Git LFS, storage not reachable from any ref, and (over MCP) truncated rows.
- All sizes are bytes; dates are UTC.

## Exit codes

| Code | Meaning |
|---|---|
| 0 | success (also an empty repository) |
| 1 | git failed or output could not be parsed |
| 2 | bad flags or arguments |
| 3 | not a git repository, or the path does not exist |
| 4 | git missing or older than 2.31 |

## Safety

- gitsize itself is safe to run anywhere: it only reads the repository. `--svg FILE` writes (and overwrites) FILE; `gitsize_svg` refuses to overwrite a file gitsize did not write.
- Do NOT run the `fix` commands on your own. They rewrite every commit id on every branch and tag, require a force-push, and every collaborator must re-clone. Show them to the user, point out that `deleted` only means absent from HEAD, and let the user decide.
- BFG matches by file name in any directory, so it can remove more than the listed paths.
