# Changelog

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
