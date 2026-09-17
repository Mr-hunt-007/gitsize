package scan

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Mr-hunt-007/gitsize/internal/analyze"
	"github.com/Mr-hunt-007/gitsize/internal/gitx"
)

const (
	kib = 1024
	mib = 1024 * 1024
)

const unicodePath = "dir with space/ünï cödé 文件.dat"

type fixture struct {
	*repo
	c1, c2, c3 string
	bigOID     string
	keepV1     string
	keepV2     string
	uniOID     string
	laterOID   string
	zerosOID   string
}

// buildFixture creates three commits over three months:
//
//	2024-01-15 c1: big.bin (1.5 MiB random), keep.bin v1 (400 KiB), unicode path (200 KiB), zeros.bin (3 MiB of zeros)
//	2024-03-10 c2: delete big.bin, keep.bin v2 (300 KiB)
//	2024-03-20 c3: later.bin (100 KiB)
func buildFixture(t *testing.T) *fixture {
	r := newRepo(t)
	f := &fixture{repo: r}
	r.write("big.bin", random(t, 1500*kib))
	r.write("keep.bin", random(t, 400*kib))
	r.write(unicodePath, random(t, 200*kib))
	r.write("zeros.bin", make([]byte, 3*mib))
	r.write("README.md", []byte("hello\n"))
	f.c1 = r.commit("2024-01-15T10:00:00Z", "first")
	f.bigOID = r.blobOID("HEAD", "big.bin")
	f.keepV1 = r.blobOID("HEAD", "keep.bin")
	f.uniOID = r.blobOID("HEAD", unicodePath)
	f.zerosOID = r.blobOID("HEAD", "zeros.bin")

	r.remove("big.bin")
	r.write("keep.bin", random(t, 300*kib))
	f.c2 = r.commit("2024-03-10T10:00:00Z", "second")
	f.keepV2 = r.blobOID("HEAD", "keep.bin")

	r.write("later.bin", random(t, 100*kib))
	f.c3 = r.commit("2024-03-20T10:00:00Z", "third")
	f.laterOID = r.blobOID("HEAD", "later.bin")
	return f
}

func TestLargestBlobs(t *testing.T) {
	f := buildFixture(t)
	for _, packed := range []bool{false, true} {
		t.Run(fmt.Sprintf("packed=%v", packed), func(t *testing.T) {
			if packed {
				f.git("gc", "-q")
			}
			rep := run(t, f.dir, ByBlob, analyze.SortDisk, false)

			wantOrder := []string{f.bigOID, f.keepV1, f.keepV2, f.uniOID, f.laterOID}
			if len(rep.Blobs) < len(wantOrder) {
				t.Fatalf("got %d blobs, want at least %d", len(rep.Blobs), len(wantOrder))
			}
			for i, oid := range wantOrder {
				if rep.Blobs[i].OID != oid {
					t.Fatalf("rank %d: got %s (%s), want %s", i, rep.Blobs[i].OID, rep.Blobs[i].Path, oid)
				}
			}
			want := map[string]struct {
				path, status, commit string
			}{
				f.bigOID:   {"big.bin", StatusDeleted, f.c1},
				f.keepV1:   {"keep.bin", StatusOldVersion, f.c1},
				f.keepV2:   {"keep.bin", StatusInHead, f.c2},
				f.uniOID:   {unicodePath, StatusInHead, f.c1},
				f.laterOID: {"later.bin", StatusInHead, f.c3},
			}
			for _, b := range rep.Blobs {
				w, ok := want[b.OID]
				if !ok {
					continue
				}
				if b.Path != w.path || b.Status != w.status {
					t.Errorf("%s: got path=%q status=%q, want %q %q", b.OID, b.Path, b.Status, w.path, w.status)
				}
				if b.Introduced == nil || b.Introduced.Commit != w.commit {
					t.Errorf("%s (%s): introduced %+v, want %s", b.OID, b.Path, b.Introduced, w.commit)
				}
			}
			if rep.Blobs[0].Size != 1500*kib || rep.Blobs[0].Disk < 1500*kib {
				t.Errorf("big.bin sizes: size=%d disk=%d", rep.Blobs[0].Size, rep.Blobs[0].Disk)
			}
			if rep.Blobs[0].Introduced.Date != "2024-01-15" {
				t.Errorf("date %q", rep.Blobs[0].Introduced.Date)
			}
			if rep.HeadTree == nil || rep.HeadTree.Files != 5 {
				t.Errorf("head tree: %+v", rep.HeadTree)
			}
			if rep.Reachable.Blobs != 7 {
				t.Errorf("reachable blobs = %d, want 7", rep.Reachable.Blobs)
			}
			if rep.Storage.Objects == 0 || rep.Storage.GitDirBytes == 0 {
				t.Errorf("storage: %+v", rep.Storage)
			}
			if packed && rep.Storage.PackedBytes == 0 {
				t.Errorf("expected packed bytes after gc: %+v", rep.Storage)
			}

			// Fix section: only big.bin is deleted and above the threshold.
			if rep.Fix == nil {
				t.Fatal("expected a fix section")
			}
			if len(rep.Fix.Paths) != 1 || rep.Fix.Paths[0] != "big.bin" {
				t.Errorf("fix paths %v", rep.Fix.Paths)
			}
			if rep.Fix.FilterRepo != "git filter-repo --invert-paths --path big.bin" {
				t.Errorf("filter-repo: %q", rep.Fix.FilterRepo)
			}
		})
	}
}

func TestSortSizeVersusDisk(t *testing.T) {
	f := buildFixture(t)
	bySize := run(t, f.dir, ByBlob, analyze.SortSize, false)
	if bySize.Blobs[0].OID != f.zerosOID {
		t.Errorf("--sort size: first is %s, want zeros.bin", bySize.Blobs[0].Path)
	}
	byDisk := run(t, f.dir, ByBlob, analyze.SortDisk, false)
	for i, b := range byDisk.Blobs[:5] {
		if b.OID == f.zerosOID {
			t.Errorf("--sort disk: compressible zeros.bin ranked %d", i)
		}
	}
	for _, b := range byDisk.Blobs {
		if b.OID == f.zerosOID && b.Disk > 64*kib {
			t.Errorf("zeros.bin disk size %d, expected heavy compression", b.Disk)
		}
	}
}

func TestByPath(t *testing.T) {
	f := buildFixture(t)
	rep := run(t, f.dir, ByPath, analyze.SortDisk, false)
	if len(rep.Blobs) != 0 {
		t.Error("largest_blobs should be empty in path mode")
	}
	if rep.Paths[0].Path != "big.bin" || rep.Paths[1].Path != "keep.bin" {
		t.Fatalf("order: %s, %s", rep.Paths[0].Path, rep.Paths[1].Path)
	}
	keep := rep.Paths[1]
	if keep.Versions != 2 || keep.Size != 700*kib || keep.Status != StatusInHead {
		t.Errorf("keep.bin: %+v", keep)
	}
	if keep.Introduced == nil || keep.Introduced.Commit != f.c1 {
		t.Errorf("keep.bin first added %+v, want %s", keep.Introduced, f.c1)
	}
	if rep.Paths[0].Status != StatusDeleted {
		t.Errorf("big.bin status %q", rep.Paths[0].Status)
	}
}

func TestByExtAndDir(t *testing.T) {
	f := buildFixture(t)
	ext := run(t, f.dir, ByExt, analyze.SortSize, false)
	got := map[string]GroupEntry{}
	for _, g := range ext.Exts {
		got[g.Key] = g
	}
	if g := got[".bin"]; g.Blobs != 5 || g.Size != (1500+400+300+100)*kib+3*mib {
		t.Errorf(".bin: %+v", g)
	}
	if g := got[".dat"]; g.Blobs != 1 {
		t.Errorf(".dat: %+v", g)
	}
	if ext.Exts[0].Key != ".bin" {
		t.Errorf("first ext %q", ext.Exts[0].Key)
	}

	dir := run(t, f.dir, ByDir, analyze.SortDisk, false)
	gotDir := map[string]GroupEntry{}
	for _, g := range dir.Dirs {
		gotDir[g.Key] = g
	}
	if g := gotDir["dir with space"]; g.Blobs != 1 || g.Size != 200*kib {
		t.Errorf("dir with space: %+v", g)
	}
	if g := gotDir[analyze.RootDir]; g.Blobs != 6 {
		t.Errorf("root: %+v", g)
	}
}

func TestHistory(t *testing.T) {
	f := buildFixture(t)
	rep := run(t, f.dir, ByBlob, analyze.SortDisk, true)
	if rep.History == nil {
		t.Fatal("no history")
	}
	months := rep.History.Months
	if len(months) != 3 {
		t.Fatalf("months: %+v", months)
	}
	if months[0].Month != "2024-01" || months[1].Month != "2024-02" || months[2].Month != "2024-03" {
		t.Errorf("months: %+v", months)
	}
	if months[0].Blobs != 5 || months[1].Blobs != 0 || months[2].Blobs != 2 {
		t.Errorf("blob counts: %+v", months)
	}
	if months[2].Size != 400*kib {
		t.Errorf("march size %d, want %d", months[2].Size, 400*kib)
	}
	if rep.History.Unattributed.Blobs != 0 {
		t.Errorf("unattributed: %+v", rep.History.Unattributed)
	}
}

func TestIntroducedOnSideBranch(t *testing.T) {
	r := newRepo(t)
	r.write("a.txt", []byte("a"))
	r.commit("2024-01-01T00:00:00Z", "root")
	r.git("checkout", "-q", "-b", "side")
	r.write("side.bin", random(t, 64*kib))
	side := r.commit("2024-02-01T00:00:00Z", "side")
	oid := r.blobOID("HEAD", "side.bin")
	r.git("checkout", "-q", "main")
	r.write("b.txt", []byte("b"))
	r.commit("2024-03-01T00:00:00Z", "main")
	r.gitEnv([]string{"GIT_AUTHOR_DATE=2024-04-01T00:00:00Z", "GIT_COMMITTER_DATE=2024-04-01T00:00:00Z"},
		"merge", "-q", "--no-ff", "--no-edit", "side")

	rep := run(t, r.dir, ByBlob, analyze.SortDisk, false)
	if rep.Blobs[0].OID != oid {
		t.Fatalf("top blob %s", rep.Blobs[0].Path)
	}
	if rep.Blobs[0].Introduced == nil || rep.Blobs[0].Introduced.Commit != side {
		t.Errorf("introduced %+v, want side commit %s", rep.Blobs[0].Introduced, side)
	}
}

func TestBlobOnlyOnOtherBranchIsDeletedFromHead(t *testing.T) {
	r := newRepo(t)
	r.write("a.txt", []byte("a"))
	r.commit("2024-01-01T00:00:00Z", "root")
	r.git("checkout", "-q", "-b", "experiment")
	r.write("dump.sql", random(t, 2*mib))
	r.commit("2024-01-02T00:00:00Z", "dump")
	r.git("checkout", "-q", "main")
	rep := run(t, r.dir, ByBlob, analyze.SortDisk, false)
	if rep.Blobs[0].Path != "dump.sql" || rep.Blobs[0].Status != StatusDeleted {
		t.Errorf("got %+v", rep.Blobs[0])
	}
}

func TestEmptyRepo(t *testing.T) {
	r := newRepo(t)
	rep := run(t, r.dir, ByBlob, analyze.SortDisk, true)
	if !rep.Repository.Empty {
		t.Error("expected empty")
	}
	if len(rep.Blobs) != 0 || rep.Fix != nil || rep.HeadTree != nil {
		t.Errorf("unexpected content: %+v", rep)
	}
}

func TestBareRepo(t *testing.T) {
	f := buildFixture(t)
	bare := filepath.Join(t.TempDir(), "mirror.git")
	f.git("clone", "-q", "--bare", f.dir, bare)
	rep := run(t, bare, ByBlob, analyze.SortDisk, false)
	if !rep.Repository.Bare {
		t.Error("expected bare")
	}
	if rep.Repository.Name != "mirror" {
		t.Errorf("name %q", rep.Repository.Name)
	}
	if rep.Blobs[0].OID != f.bigOID || rep.Blobs[0].Status != StatusDeleted {
		t.Errorf("top blob %+v", rep.Blobs[0])
	}
	if rep.Blobs[0].Introduced == nil || rep.Blobs[0].Introduced.Commit != f.c1 {
		t.Errorf("introduced %+v", rep.Blobs[0].Introduced)
	}
}

func TestShallowClone(t *testing.T) {
	f := buildFixture(t)
	dst := filepath.Join(t.TempDir(), "shallow")
	url := "file://" + filepath.ToSlash(f.dir)
	if runtime.GOOS == "windows" {
		url = "file:///" + filepath.ToSlash(f.dir)
	}
	f.git("clone", "-q", "--depth", "1", url, dst)
	rep := run(t, dst, ByBlob, analyze.SortDisk, false)
	if !rep.Repository.Shallow {
		t.Fatal("expected shallow")
	}
	if !containsNote(rep, "Shallow clone") {
		t.Errorf("notes: %v", rep.Notes)
	}
	for _, b := range rep.Blobs {
		if b.OID == f.bigOID {
			t.Error("deleted blob should not be present in a depth-1 clone")
		}
	}
	// In a shallow clone the boundary commit introduces everything it has.
	if rep.Blobs[0].Introduced == nil || rep.Blobs[0].Introduced.Commit != f.c3 {
		t.Errorf("introduced %+v, want boundary %s", rep.Blobs[0].Introduced, f.c3)
	}
}

func TestLFSPointers(t *testing.T) {
	r := newRepo(t)
	r.write(".gitattributes", []byte("*.psd filter=lfs diff=lfs merge=lfs -text\n"))
	pointer := "version https://git-lfs.github.com/spec/v1\n" +
		"oid sha256:4d7a214614ab2935c943f9e0ff69d22eadbb8f32b1258daaa5e2ca24d17e2393\n" +
		"size 123456789\n"
	r.write("art/cover.psd", []byte(pointer))
	r.write("notes.txt", bytes.Repeat([]byte("x"), 150))
	r.commit("2024-01-01T00:00:00Z", "lfs")
	rep := run(t, r.dir, ByBlob, analyze.SortDisk, false)
	if rep.LFS == nil {
		t.Fatal("LFS not detected")
	}
	if rep.LFS.PointerBlobs != 1 || rep.LFS.PointersInHead != 1 || rep.LFS.LFSContentBytes != 123456789 || !rep.LFS.AttributesInHead {
		t.Errorf("lfs: %+v", rep.LFS)
	}
	for _, b := range rep.Blobs {
		if (b.Path == "art/cover.psd") != b.LFS {
			t.Errorf("lfs flag wrong for %s: %v", b.Path, b.LFS)
		}
	}
	if !containsNote(rep, "Git LFS") {
		t.Errorf("notes: %v", rep.Notes)
	}
}

func TestNoLFS(t *testing.T) {
	f := buildFixture(t)
	rep := run(t, f.dir, ByBlob, analyze.SortDisk, false)
	if rep.LFS != nil {
		t.Errorf("unexpected lfs %+v", rep.LFS)
	}
}

func TestNewlineInPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file names cannot contain newlines on Windows")
	}
	requireGit(t)
	v, err := gitx.GitVersion(context.Background())
	if err != nil || !v.AtLeast(2, 50) {
		t.Skip("NUL-delimited rev-list needs git 2.50+")
	}
	r := newRepo(t)
	name := "weird\nname.bin"
	r.write(name, random(t, 64*kib))
	c := r.commit("2024-01-01T00:00:00Z", "weird")
	rep := run(t, r.dir, ByBlob, analyze.SortDisk, false)
	if rep.Blobs[0].Path != name || rep.Blobs[0].Status != StatusInHead {
		t.Errorf("got %+v", rep.Blobs[0])
	}
	if rep.Blobs[0].Introduced == nil || rep.Blobs[0].Introduced.Commit != c {
		t.Errorf("introduced %+v", rep.Blobs[0].Introduced)
	}
}

func TestFixSkipsPathThatIsNowADirectory(t *testing.T) {
	r := newRepo(t)
	r.write("data", random(t, 2*mib))
	r.commit("2024-01-01T00:00:00Z", "file")
	r.remove("data")
	r.write("data/small.txt", []byte("x"))
	r.commit("2024-01-02T00:00:00Z", "dir")
	rep := run(t, r.dir, ByBlob, analyze.SortDisk, false)
	if rep.Fix != nil {
		t.Errorf("fix should not suggest a path that is a directory in HEAD: %+v", rep.Fix)
	}
}

func TestNotARepo(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	_, err := Run(Config{Dir: dir, Largest: 10, Sort: analyze.SortDisk, By: ByBlob})
	if !errors.Is(err, ErrNotRepo) {
		t.Errorf("err = %v, want ErrNotRepo", err)
	}
	_, err = Run(Config{Dir: filepath.Join(dir, "missing"), Largest: 10, Sort: analyze.SortDisk, By: ByBlob})
	if !errors.Is(err, ErrNotRepo) {
		t.Errorf("missing dir: err = %v", err)
	}
}

func TestRunFromSubdirectory(t *testing.T) {
	f := buildFixture(t)
	sub := filepath.Join(f.dir, "dir with space")
	rep := run(t, sub, ByBlob, analyze.SortDisk, false)
	if rep.HeadTree.Files != 5 || rep.Blobs[0].OID != f.bigOID {
		t.Errorf("subdir scan: files=%d top=%s", rep.HeadTree.Files, rep.Blobs[0].Path)
	}
	if rep.Repository.Name != filepath.Base(f.dir) {
		t.Errorf("name %q", rep.Repository.Name)
	}
}

func TestUnreachableNote(t *testing.T) {
	r := newRepo(t)
	r.write("a.txt", []byte("a"))
	r.commit("2024-01-01T00:00:00Z", "root")
	r.write("huge.bin", random(t, 3*mib))
	r.commit("2024-01-02T00:00:00Z", "oops")
	r.git("reset", "-q", "--hard", "HEAD~1")
	rep := run(t, r.dir, ByBlob, analyze.SortDisk, false)
	for _, b := range rep.Blobs {
		if b.Path == "huge.bin" {
			t.Error("reflog-only blob must not be listed")
		}
	}
	if !containsNote(rep, "not reachable") {
		t.Errorf("expected unreachable note, got %v", rep.Notes)
	}
}

func containsNote(rep *Report, s string) bool {
	for _, n := range rep.Notes {
		if strings.Contains(n, s) {
			return true
		}
	}
	return false
}

func TestObjectStoreBytesMissingDir(t *testing.T) {
	if got := objectStoreBytes(filepath.Join(t.TempDir(), "nope")); got != 0 {
		t.Errorf("got %d", got)
	}
	d := t.TempDir()
	os.MkdirAll(filepath.Join(d, "ab"), 0o755)
	os.WriteFile(filepath.Join(d, "ab", "cdef"), make([]byte, 100), 0o644)
	os.MkdirAll(filepath.Join(d, "info"), 0o755)
	os.WriteFile(filepath.Join(d, "info", "packs"), make([]byte, 50), 0o644)
	if got := objectStoreBytes(d); got != 100 {
		t.Errorf("got %d, want 100", got)
	}
}

func TestLegacyNewlineRevList(t *testing.T) {
	f := buildFixture(t)
	modern := run(t, f.dir, ByBlob, analyze.SortDisk, true)
	legacy, err := Run(Config{Dir: f.dir, Largest: 10, Sort: analyze.SortDisk, By: ByBlob, History: true, legacyRevList: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(modern.Blobs) != len(legacy.Blobs) {
		t.Fatalf("blob count differs: %d vs %d", len(modern.Blobs), len(legacy.Blobs))
	}
	for i := range modern.Blobs {
		m, l := modern.Blobs[i], legacy.Blobs[i]
		if m.OID != l.OID || m.Path != l.Path || m.Status != l.Status || m.Disk != l.Disk {
			t.Errorf("rank %d differs: %+v vs %+v", i, m, l)
		}
	}
	if modern.Reachable != legacy.Reachable {
		t.Errorf("reachable differs: %+v vs %+v", modern.Reachable, legacy.Reachable)
	}
}

func TestAvailableCounts(t *testing.T) {
	f := buildFixture(t)
	tests := []struct {
		by   string
		rows int64
	}{
		{ByBlob, 7}, // 7 blob versions in history
		{ByPath, 6},
		{ByExt, 3}, // .bin, .dat, .md
	}
	for _, tt := range tests {
		rep, err := Run(Config{Dir: f.dir, Largest: 1, Sort: analyze.SortDisk, By: tt.by})
		if err != nil {
			t.Fatal(err)
		}
		if rep.Available.Rows != tt.rows {
			t.Errorf("%s: available rows %d, want %d", tt.by, rep.Available.Rows, tt.rows)
		}
		if rep.Available.FixPaths != 1 || rep.Fix == nil || len(rep.Fix.Paths) != 1 {
			t.Errorf("%s: fix paths available %d, fix %+v", tt.by, rep.Available.FixPaths, rep.Fix)
		}
	}
}

func TestRunContextCancelled(t *testing.T) {
	f := buildFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rep, err := RunContext(ctx, Config{Dir: f.dir, Largest: 10, Sort: analyze.SortDisk, By: ByBlob})
	if !errors.Is(err, context.Canceled) || rep != nil {
		t.Errorf("cancelled scan: rep=%v err=%v", rep != nil, err)
	}
}
