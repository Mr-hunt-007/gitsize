package scan

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Mr-hunt-007/gitsize/internal/analyze"
	"github.com/Mr-hunt-007/gitsize/internal/gitx"
)

// Errors the CLI maps to exit codes.
var (
	ErrNotRepo  = errors.New("not a git repository")
	ErrOldGit   = errors.New("git 2.31 or newer is required")
	ErrGitNotOK = gitx.ErrGitNotFound
)

// Grouping modes for --by.
const (
	ByBlob = "blob"
	ByPath = "path"
	ByExt  = "ext"
	ByDir  = "dir"
)

// FixThreshold is the minimum on-disk size of a deleted path's history for it
// to be suggested in the "how to fix" section.
const FixThreshold = 1 << 20

// Config controls a scan.
type Config struct {
	Dir     string
	Largest int
	Sort    analyze.SortKey
	By      string
	History bool

	// legacyRevList forces the newline-delimited rev-list path used on git
	// older than 2.50 (for tests).
	legacyRevList bool
}

type scanner struct {
	cfg     Config
	git     gitx.Runner
	version gitx.Version
	rep     *Report

	headPaths map[string]string // path -> blob oid at HEAD
	headOIDs  map[string]bool
	headDirs  map[string]bool
	lfsBlobs  map[string]bool // blob oids that are LFS pointer files
}

// Run scans the repository described by cfg.
func Run(cfg Config) (*Report, error) {
	v, err := gitx.GitVersion()
	if err != nil {
		return nil, err
	}
	if !v.AtLeast(2, 31) {
		return nil, fmt.Errorf("%w (found %s)", ErrOldGit, v)
	}
	s := &scanner{
		cfg:       cfg,
		git:       gitx.Runner{Dir: cfg.Dir},
		version:   v,
		headPaths: map[string]string{},
		headOIDs:  map[string]bool{},
		headDirs:  map[string]bool{},
		lfsBlobs:  map[string]bool{},
		rep: &Report{
			Schema: SchemaVersion,
			Options: Options{
				Largest: cfg.Largest, Sort: string(cfg.Sort), By: cfg.By, History: cfg.History,
			},
			Notes: []string{},
		},
	}
	s.rep.Repository.GitVersion = v.String()
	if err := s.run(); err != nil {
		return nil, err
	}
	return s.rep, nil
}

func (s *scanner) note(format string, args ...any) {
	s.rep.Notes = append(s.rep.Notes, fmt.Sprintf(format, args...))
}

func (s *scanner) run() error {
	if fi, err := os.Stat(s.cfg.Dir); err != nil || !fi.IsDir() {
		return fmt.Errorf("%w: %s is not a directory", ErrNotRepo, s.cfg.Dir)
	}
	if err := s.repoInfo(); err != nil {
		return err
	}
	if err := s.storage(); err != nil {
		return err
	}
	if s.rep.Repository.Empty {
		s.note("The repository has no commits yet.")
		return nil
	}
	if s.rep.Repository.HeadResolves {
		if err := s.headTree(); err != nil {
			return err
		}
	} else {
		s.note("HEAD does not point to a commit, so in-HEAD status is unknown and no fix is suggested.")
	}
	lfsLikely, err := s.lfsLikely()
	if err != nil {
		return err
	}
	st, err := s.objects(lfsLikely)
	if err != nil {
		return err
	}
	if lfsLikely {
		if err := s.lfs(st.lfsCandidates); err != nil {
			return err
		}
	}
	return s.results(st)
}

func (s *scanner) repoInfo() error {
	out, err := s.git.Output("rev-parse", "--is-bare-repository", "--is-shallow-repository",
		"--path-format=absolute", "--git-common-dir")
	if err != nil {
		var ge *gitx.Error
		if errors.As(err, &ge) {
			return fmt.Errorf("%w: %s", ErrNotRepo, strings.TrimSpace(ge.Stderr))
		}
		return err
	}
	lines := strings.Split(strings.TrimRight(strings.ReplaceAll(string(out), "\r\n", "\n"), "\n"), "\n")
	if len(lines) < 3 {
		return fmt.Errorf("unexpected rev-parse output %q", out)
	}
	r := &s.rep.Repository
	r.Bare = lines[0] == "true"
	r.Shallow = lines[1] == "true"
	r.GitDir = filepath.Clean(lines[2])
	if !r.Bare {
		if top, err := s.git.Output("rev-parse", "--show-toplevel"); err == nil {
			r.WorkTree = filepath.Clean(strings.TrimSpace(string(top)))
		}
	}
	switch {
	case r.WorkTree != "":
		r.Name = filepath.Base(r.WorkTree)
	case strings.EqualFold(filepath.Base(r.GitDir), ".git"):
		r.Name = filepath.Base(filepath.Dir(r.GitDir))
	default:
		r.Name = strings.TrimSuffix(filepath.Base(r.GitDir), ".git")
	}

	if _, err := s.git.Output("rev-parse", "--verify", "-q", "HEAD^{commit}"); err == nil {
		r.HeadResolves = true
	}
	refs, err := s.git.Output("for-each-ref", "--count=1", "--format=%(refname)")
	if err != nil {
		return err
	}
	r.Empty = !r.HeadResolves && len(bytes.TrimSpace(refs)) == 0

	if cfg, err := s.git.Output("config", "--get-regexp", `^(extensions\.partialclone|remote\..*\.promisor)$`); err == nil && len(bytes.TrimSpace(cfg)) > 0 {
		r.PartialClone = true
	}
	if r.Shallow {
		s.note("Shallow clone: history below the shallow boundary is not present locally, so sizes, largest objects, introducing commits and growth are partial. The boundary commit appears to introduce every file it contains.")
	}
	return nil
}

func (s *scanner) storage() error {
	out, err := s.git.Output("count-objects", "-v")
	if err != nil {
		return err
	}
	c, err := analyze.ParseCountObjects(string(out))
	if err != nil {
		return err
	}
	s.rep.Counts = c
	st := &s.rep.Storage
	st.PackedBytes = c.PackedBytes
	st.LooseBytes = c.LooseBytes
	st.Objects = c.LooseObjects + c.PackedObjects
	st.GitDirBytes = dirUsage(s.rep.Repository.GitDir)
	st.LFSCacheBytes = dirUsage(filepath.Join(s.rep.Repository.GitDir, "lfs"))
	if c.Garbage > 0 {
		s.note("count-objects reports %d garbage file(s) in the object directory (%d bytes).", c.Garbage, c.GarbageBytes)
	}
	return nil
}

// dirSize sums the apparent size of regular files under dir without following
// symlinks. Unreadable entries are skipped.
func dirSize(dir string) int64 { return walkSize(dir, false) }

// dirUsage is dirSize measured in allocated disk blocks, like du.
func dirUsage(dir string) int64 { return walkSize(dir, true) }

func walkSize(dir string, usage bool) int64 {
	var total int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				if usage {
					total += diskUsage(info)
				} else {
					total += info.Size()
				}
			}
		}
		return nil
	})
	return total
}

func (s *scanner) headTree() error {
	ht := &HeadTree{}
	err := s.git.Stream(nil, func(r io.Reader) error {
		return analyze.ScanRecords(r, 0, func(rec string) error {
			e, err := analyze.ParseLsTree(rec)
			if err != nil {
				return err
			}
			if e.Type != "blob" {
				return nil
			}
			ht.Files++
			if e.Size < 0 {
				ht.SizesUnknown++
			} else {
				ht.Bytes += e.Size
			}
			s.headPaths[e.Path] = e.OID
			s.headOIDs[e.OID] = true
			for d := path.Dir(e.Path); d != "." && d != "/" && !s.headDirs[d]; d = path.Dir(d) {
				s.headDirs[d] = true
			}
			return nil
		})
	}, "ls-tree", "-r", "-l", "-z", "--full-tree", "HEAD")
	if err != nil {
		return err
	}
	s.rep.HeadTree = ht
	return nil
}

// lfsLikely decides whether to spend a pass reading small blobs to find LFS
// pointers: only when a local LFS store exists or HEAD's .gitattributes
// configure the lfs filter.
func (s *scanner) lfsLikely() (bool, error) {
	var attrs []string
	for p, oid := range s.headPaths {
		if path.Base(p) == ".gitattributes" {
			attrs = append(attrs, oid)
		}
	}
	inHead := false
	if len(attrs) > 0 {
		err := s.catFileBatch(attrs, func(_ string, content []byte) error {
			if bytes.Contains(content, []byte("filter=lfs")) {
				inHead = true
			}
			return nil
		})
		if err != nil {
			return false, err
		}
	}
	if inHead {
		s.rep.LFS = &LFS{AttributesInHead: true}
	}
	if fi, err := os.Stat(filepath.Join(s.rep.Repository.GitDir, "lfs")); inHead || (err == nil && fi.IsDir()) {
		return true, nil
	}
	return false, nil
}

type objectStats struct {
	agg           *analyze.Aggregator
	sizes         map[string][2]int64 // only with --history
	lfsCandidates []string
}

func (s *scanner) objects(lfsLikely bool) (*objectStats, error) {
	n := s.cfg.Largest
	if s.cfg.By != ByBlob {
		n = 0
	}
	st := &objectStats{agg: analyze.NewAggregator(s.cfg.Sort, n)}
	if s.cfg.History {
		st.sizes = map[string][2]int64{}
	}
	nul := s.version.AtLeast(2, 50) && !s.cfg.legacyRevList // rev-list -z --objects arrived in git 2.50
	revArgs := []string{"rev-list", "--objects", "--all"}
	catArgs := []string{"cat-file", "--buffer", "--batch-check=" + analyze.BatchCheckFormat}
	var sep byte = '\n'
	if nul {
		revArgs = append(revArgs, "-z")
		catArgs = append(catArgs, "-Z")
		sep = 0
	}
	if s.rep.Repository.PartialClone {
		revArgs = append(revArgs, "--missing=allow-promisor")
	}
	reach := &s.rep.Reachable

	feed := func(w io.Writer) error {
		bw := bufio.NewWriterSize(w, 256*1024)
		err := s.git.Stream(nil, func(r io.Reader) error {
			return analyze.RevListToBatchInput(r, nul, func(rec []byte) error {
				if _, err := bw.Write(rec); err != nil {
					return err
				}
				return bw.WriteByte(sep)
			})
		}, revArgs...)
		if err != nil {
			return err
		}
		return bw.Flush()
	}
	consume := func(r io.Reader) error {
		return analyze.ScanRecords(r, sep, func(rec string) error {
			o, err := analyze.ParseBatchCheck(rec)
			if err != nil {
				return err
			}
			if o.Missing {
				reach.Missing++
				return nil
			}
			reach.Objects++
			reach.DiskBytes += o.Disk
			if o.Type != "blob" {
				return nil
			}
			reach.Blobs++
			reach.BlobBytes += o.Size
			reach.BlobDisk += o.Disk
			st.agg.Add(analyze.Blob{OID: o.OID, Path: o.Path, Size: o.Size, Disk: o.Disk})
			if st.sizes != nil {
				st.sizes[o.OID] = [2]int64{o.Size, o.Disk}
			}
			if lfsLikely && o.Size < 1024 && o.Size >= 100 {
				st.lfsCandidates = append(st.lfsCandidates, o.OID)
			}
			return nil
		})
	}
	if err := s.git.Stream(feed, consume, catArgs...); err != nil {
		return nil, err
	}
	switch {
	case reach.Missing > 0:
		s.note("Partial clone: %d object(s) are not present locally and were skipped, so blob totals are partial. gitsize never fetches missing objects.", reach.Missing)
	case s.rep.Repository.PartialClone:
		s.note("Partial clone: objects filtered out of the clone are not listed, so blob totals may be partial. gitsize never fetches missing objects.")
	}
	storage := objectStoreBytes(filepath.Join(s.rep.Repository.GitDir, "objects"))
	if gap := storage - reach.DiskBytes; gap > 1<<20 && gap > storage/20 {
		s.note("About %s of object storage is not reachable from any branch, tag or HEAD (reflog-only commits, deleted branches, duplicate packs, or objects not yet pruned) and is not listed here. git gc reclaims unreachable objects only after their reflog entries expire.", analyze.HumanBytes(gap))
	}
	return st, nil
}

// catFileBatch reads full object contents through one `git cat-file --batch`.
func (s *scanner) catFileBatch(oids []string, fn func(oid string, content []byte) error) error {
	feed := func(w io.Writer) error {
		bw := bufio.NewWriter(w)
		for _, o := range oids {
			if _, err := bw.WriteString(o + "\n"); err != nil {
				return err
			}
		}
		return bw.Flush()
	}
	consume := func(r io.Reader) error {
		br := bufio.NewReaderSize(r, 64*1024)
		for {
			header, err := br.ReadString('\n')
			if err == io.EOF && header == "" {
				return nil
			}
			if err != nil {
				return err
			}
			f := strings.Fields(header)
			if len(f) == 2 && f[1] == "missing" {
				continue
			}
			if len(f) != 3 {
				return fmt.Errorf("cat-file --batch: malformed header %q", header)
			}
			size, err := strconv.ParseInt(f[2], 10, 64)
			if err != nil || size < 0 {
				return fmt.Errorf("cat-file --batch: bad size in %q", header)
			}
			buf := make([]byte, size+1)
			if _, err := io.ReadFull(br, buf); err != nil {
				return err
			}
			if err := fn(f[0], buf[:size]); err != nil {
				return err
			}
		}
	}
	return s.git.Stream(feed, consume, "cat-file", "--batch")
}

func (s *scanner) lfs(candidates []string) error {
	seen := map[string]bool{}
	info := s.rep.LFS
	if info == nil {
		info = &LFS{}
	}
	err := s.catFileBatch(candidates, func(oid string, content []byte) error {
		p, ok := analyze.ParseLFSPointer(content)
		if !ok {
			return nil
		}
		info.PointerBlobs++
		s.lfsBlobs[oid] = true
		if s.headOIDs[oid] {
			info.PointersInHead++
		}
		if !seen[p.OID] {
			seen[p.OID] = true
			info.LFSObjects++
			info.LFSContentBytes += p.Size
		}
		return nil
	})
	if err != nil {
		return err
	}
	if info.PointerBlobs > 0 || info.AttributesInHead {
		s.rep.LFS = info
	}
	if info.PointerBlobs > 0 {
		s.note("Git LFS: %d pointer blob(s) in history reference %d LFS object(s) totalling %s. That content lives in LFS storage, not in the git objects measured here.", info.PointerBlobs, info.LFSObjects, analyze.HumanBytes(info.LFSContentBytes))
	}
	return nil
}

func (s *scanner) blobStatus(p, oid string) string {
	if !s.rep.Repository.HeadResolves {
		return StatusUnknown
	}
	if s.headPaths[p] == oid || s.headOIDs[oid] {
		return StatusInHead
	}
	if _, ok := s.headPaths[p]; ok {
		return StatusOldVersion
	}
	return StatusDeleted
}

func (s *scanner) pathStatus(p string) string {
	if !s.rep.Repository.HeadResolves {
		return StatusUnknown
	}
	if _, ok := s.headPaths[p]; ok {
		return StatusInHead
	}
	return StatusDeleted
}

func (s *scanner) results(st *objectStats) error {
	n := s.cfg.Largest
	wantBlobs := map[string]*analyze.Intro{}
	wantPaths := map[string]*analyze.Intro{}

	switch s.cfg.By {
	case ByBlob:
		for _, b := range st.agg.TopBlobs() {
			s.rep.Blobs = append(s.rep.Blobs, BlobEntry{
				Path: b.Path, OID: b.OID, Size: b.Size, Disk: b.Disk,
				Status: s.blobStatus(b.Path, b.OID),
				LFS:    s.lfsBlobs[b.OID],
			})
			wantBlobs[b.OID] = &analyze.Intro{}
		}
	case ByPath:
		for _, g := range head(st.agg.Paths(), n) {
			s.rep.Paths = append(s.rep.Paths, PathEntry{
				Path: g.Key, Versions: g.Blobs, Size: g.Size, Disk: g.Disk,
				Status: s.pathStatus(g.Key),
			})
			wantPaths[g.Key] = &analyze.Intro{}
		}
	case ByExt:
		s.rep.Exts = groupEntries(head(st.agg.Exts(), n))
	case ByDir:
		s.rep.Dirs = groupEntries(head(st.agg.Dirs(), n))
	}

	// Fix candidates: deleted paths whose history is big. A path that still
	// exists in HEAD as a file or directory is never suggested, because
	// --path would remove it from HEAD too.
	if s.rep.Repository.HeadResolves {
		var fix []analyze.Group
		for _, g := range st.agg.Paths() {
			if g.Disk < FixThreshold {
				continue
			}
			if _, inHead := s.headPaths[g.Key]; inHead || s.headDirs[g.Key] {
				continue
			}
			fix = append(fix, g)
		}
		sort.SliceStable(fix, func(i, j int) bool { return fix[i].Disk > fix[j].Disk })
		s.rep.Fix = analyze.BuildFix(head(fix, n))
	}

	var first map[string]int64
	if s.cfg.History {
		first = make(map[string]int64, len(st.sizes))
	}
	if len(wantBlobs) > 0 || len(wantPaths) > 0 || first != nil {
		err := s.git.Stream(nil, func(r io.Reader) error {
			return analyze.ParseRawLog(r, func(c analyze.LogCommit, ch analyze.LogChange) error {
				if ch.DstMode == "160000" || analyze.IsZeroOID(ch.DstOID) {
					return nil
				}
				if in := wantBlobs[ch.DstOID]; in != nil && in.Earlier(c.Hash, c.Time) {
					*in = analyze.Intro{Commit: c.Hash, Time: c.Time}
				}
				if in := wantPaths[ch.Path]; in != nil && in.Earlier(c.Hash, c.Time) {
					*in = analyze.Intro{Commit: c.Hash, Time: c.Time}
				}
				if first != nil {
					if _, isBlob := st.sizes[ch.DstOID]; isBlob {
						if t, ok := first[ch.DstOID]; !ok || c.Time < t {
							first[ch.DstOID] = c.Time
						}
					}
				}
				return nil
			})
		}, "-c", "log.showRoot=true", "log", "--all", "--format="+analyze.LogFormat, "--raw", "--no-abbrev", "-z",
			"--no-renames", "--diff-merges=first-parent", "--no-show-signature", "--no-color")
		if err != nil {
			return err
		}
	}
	for i := range s.rep.Blobs {
		s.rep.Blobs[i].Introduced = introduced(wantBlobs[s.rep.Blobs[i].OID])
	}
	for i := range s.rep.Paths {
		s.rep.Paths[i].Introduced = introduced(wantPaths[s.rep.Paths[i].Path])
	}
	if first != nil {
		months, un := analyze.BuildHistory(st.sizes, first)
		s.rep.History = &History{Months: months, Unattributed: GroupEntry{Key: "unattributed", Blobs: un.Blobs, Size: un.Size, Disk: un.Disk}}
		if months == nil {
			s.rep.History.Months = []analyze.Month{}
		}
	}
	return nil
}

func head(gs []analyze.Group, n int) []analyze.Group {
	if len(gs) > n {
		return gs[:n]
	}
	return gs
}

func groupEntries(gs []analyze.Group) []GroupEntry {
	out := make([]GroupEntry, 0, len(gs))
	for _, g := range gs {
		out = append(out, GroupEntry{Key: g.Key, Blobs: g.Blobs, Size: g.Size, Disk: g.Disk})
	}
	return out
}

func introduced(in *analyze.Intro) *Introduced {
	if in == nil || in.Commit == "" {
		return nil
	}
	return &Introduced{Commit: in.Commit, Time: in.Time, Date: time.Unix(in.Time, 0).UTC().Format("2006-01-02")}
}

// objectStoreBytes returns the object bytes git can attribute to individual
// objects: loose object files plus pack data (pack size minus header and
// trailer). This is directly comparable to summed %(objectsize:disk).
func objectStoreBytes(objects string) int64 {
	var total int64
	entries, err := os.ReadDir(objects)
	if err != nil {
		return 0
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() && len(name) == 2 && isHex(name) {
			total += dirSize(filepath.Join(objects, name))
		}
	}
	packs, _ := filepath.Glob(filepath.Join(objects, "pack", "*.pack"))
	for _, p := range packs {
		if fi, err := os.Stat(p); err == nil && fi.Size() > 32 {
			// 12-byte header plus a 20-byte (SHA-1) or 32-byte (SHA-256) trailer.
			total += fi.Size() - 12 - 20
		}
	}
	return total
}

func isHex(s string) bool {
	for _, c := range s {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}
