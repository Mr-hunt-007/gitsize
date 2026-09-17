package analyze

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

func TestParseCountObjects(t *testing.T) {
	in := "count: 921\nsize: 9247\nin-pack: 227693\npacks: 8\nsize-pack: 534569\nprune-packable: 39\ngarbage: 1\nsize-garbage: 2\n"
	got, err := ParseCountObjects(in)
	if err != nil {
		t.Fatal(err)
	}
	want := CountObjects{LooseObjects: 921, LooseBytes: 9247 * 1024, PackedObjects: 227693, Packs: 8,
		PackedBytes: 534569 * 1024, PrunePackable: 39, Garbage: 1, GarbageBytes: 2048}
	if got != want {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
	if _, err := ParseCountObjects("count: 1\r\nsize: 4\r\n"); err != nil {
		t.Errorf("CRLF: %v", err)
	}
	for _, bad := range []string{"", "nonsense", "count: x"} {
		if _, err := ParseCountObjects(bad); err == nil {
			t.Errorf("%q: expected error", bad)
		}
	}
}

func TestParseBatchCheck(t *testing.T) {
	tests := []struct {
		in      string
		want    Object
		wantErr bool
	}{
		{"blob abc 100 40 path/to/file.txt", Object{Type: "blob", OID: "abc", Size: 100, Disk: 40, Path: "path/to/file.txt"}, false},
		{"blob abc 100 40 dir with space/ünï.txt", Object{Type: "blob", OID: "abc", Size: 100, Disk: 40, Path: "dir with space/ünï.txt"}, false},
		{"blob abc 1 1  leading space", Object{Type: "blob", OID: "abc", Size: 1, Disk: 1, Path: " leading space"}, false},
		{"blob abc 5 5 line\nbreak", Object{Type: "blob", OID: "abc", Size: 5, Disk: 5, Path: "line\nbreak"}, false},
		{"commit abc 220 150 ", Object{Type: "commit", OID: "abc", Size: 220, Disk: 150}, false},
		{"commit abc 220 150", Object{Type: "commit", OID: "abc", Size: 220, Disk: 150}, false},
		{"abc missing", Object{OID: "abc", Missing: true}, false},
		{"blob abc 1 1 missing", Object{Type: "blob", OID: "abc", Size: 1, Disk: 1, Path: "missing"}, false},
		{"abc ambiguous", Object{}, true},
		{"blob abc x 1 p", Object{}, true},
		{"blob abc 1 y p", Object{}, true},
		{"garbage", Object{}, true},
	}
	for _, tt := range tests {
		got, err := ParseBatchCheck(tt.in)
		if (err != nil) != tt.wantErr {
			t.Errorf("%q: err=%v", tt.in, err)
			continue
		}
		if !tt.wantErr && got != tt.want {
			t.Errorf("%q: got %+v want %+v", tt.in, got, tt.want)
		}
	}
}

func collectRevList(t *testing.T, in string, nul bool) []string {
	t.Helper()
	var out []string
	err := RevListToBatchInput(strings.NewReader(in), nul, func(rec []byte) error {
		out = append(out, string(rec))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestRevListToBatchInput(t *testing.T) {
	nulIn := "c1\x00t1\x00path=\x00b1\x00path=a b/ünï\x00b2\x00path=new\nline\x00b3\x00path=x\x00missing=yes\x00"
	got := collectRevList(t, nulIn, true)
	want := []string{"c1", "t1 ", "b1 a b/ünï", "b2 new\nline", "b3 x"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("nul: got %q want %q", got, want)
	}
	lineIn := "c1\nt1 \nb1 a b/ünï\r\n\nb2 x\n"
	got = collectRevList(t, lineIn, false)
	want = []string{"c1", "t1 ", "b1 a b/ünï", "b2 x"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("lines: got %q want %q", got, want)
	}
	if err := RevListToBatchInput(strings.NewReader("path=x\x00"), true, func([]byte) error { return nil }); err == nil {
		t.Error("expected error for orphan path token")
	}
}

func TestScanRecords(t *testing.T) {
	var got []string
	_ = ScanRecords(strings.NewReader("a\x00b c\x00\x00d"), 0, func(r string) error { got = append(got, r); return nil })
	if !reflect.DeepEqual(got, []string{"a", "b c", "d"}) {
		t.Errorf("nul: %q", got)
	}
	got = nil
	_ = ScanRecords(strings.NewReader("a\r\nb\n"), '\n', func(r string) error { got = append(got, r); return nil })
	if !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Errorf("lines: %q", got)
	}
}

func TestParseLsTree(t *testing.T) {
	tests := []struct {
		in      string
		want    TreeEntry
		wantErr bool
	}{
		{"100644 blob abc    1234\tdir with space/f é.txt", TreeEntry{"100644", "blob", "abc", 1234, "dir with space/f é.txt"}, false},
		{"160000 commit def       -\tvendor/sub", TreeEntry{"160000", "commit", "def", -1, "vendor/sub"}, false},
		{"100644 blob abc 5\tpath\twith\ttabs", TreeEntry{"100644", "blob", "abc", 5, "path\twith\ttabs"}, false},
		{"100644 blob abc     BAD\t.gitignore", TreeEntry{"100644", "blob", "abc", -1, ".gitignore"}, false},
		{"100644 blob abc 5 no-tab", TreeEntry{}, true},
		{"100644 blob abc x\tp", TreeEntry{}, true},
	}
	for _, tt := range tests {
		got, err := ParseLsTree(tt.in)
		if (err != nil) != tt.wantErr {
			t.Errorf("%q: err=%v", tt.in, err)
			continue
		}
		if !tt.wantErr && got != tt.want {
			t.Errorf("%q: got %+v want %+v", tt.in, got, tt.want)
		}
	}
}

func TestParseRawLog(t *testing.T) {
	z := strings.Repeat("0", 40)
	in := "c2 200\x00\n:100644 100644 " + z + " b2 M\x00a b/ü.txt\x00" +
		":100644 000000 b1 " + z + " D\x00gone.bin\x00" +
		"merge 300\x00" + // no changes
		"c1 100\x00\n:000000 100644 " + z + " b1 A\x00:looks like meta\x00" +
		":000000 160000 " + z + " s1 A\x00sub\x00"
	type row struct {
		commit string
		time   int64
		ch     LogChange
	}
	var got []row
	err := ParseRawLog(strings.NewReader(in), func(c LogCommit, ch LogChange) error {
		got = append(got, row{c.Hash, c.Time, ch})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []row{
		{"c2", 200, LogChange{"100644", "b2", "M", "a b/ü.txt"}},
		{"c2", 200, LogChange{"000000", z, "D", "gone.bin"}},
		{"c1", 100, LogChange{"100644", "b1", "A", ":looks like meta"}},
		{"c1", 100, LogChange{"160000", "s1", "A", "sub"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
	for _, bad := range []string{"nospace\x00", "c1 notanumber\x00", "c1 1\x00:too few\x00p\x00"} {
		if err := ParseRawLog(strings.NewReader(bad), func(LogCommit, LogChange) error { return nil }); err == nil {
			t.Errorf("%q: expected error", bad)
		}
	}
}

func TestIsZeroOID(t *testing.T) {
	if !IsZeroOID(strings.Repeat("0", 40)) || !IsZeroOID(strings.Repeat("0", 64)) || IsZeroOID("0a00") {
		t.Error("IsZeroOID")
	}
}

func TestParseLFSPointer(t *testing.T) {
	good := "version https://git-lfs.github.com/spec/v1\noid sha256:abc\nsize 12345\n"
	tests := []struct {
		in   string
		want LFSPointer
		ok   bool
	}{
		{good, LFSPointer{"sha256:abc", 12345}, true},
		{strings.ReplaceAll(good, "\n", "\r\n"), LFSPointer{"sha256:abc", 12345}, true},
		{"version https://git-lfs.github.com/spec/v1\noid sha256:abc\n", LFSPointer{}, false},
		{"version https://git-lfs.github.com/spec/v1\nsize 1\n", LFSPointer{}, false},
		{"version https://git-lfs.github.com/spec/v1\noid sha256:abc\nsize -1\n", LFSPointer{}, false},
		{"hello world", LFSPointer{}, false},
		{good + strings.Repeat("x", 1024), LFSPointer{}, false},
	}
	for _, tt := range tests {
		got, ok := ParseLFSPointer([]byte(tt.in))
		if ok != tt.ok || got != tt.want {
			t.Errorf("%q: got %+v %v", tt.in, got, ok)
		}
	}
}

func TestExtAndDir(t *testing.T) {
	exts := map[string]string{
		"a/b/file.TXT":   ".txt",
		"archive.tar.gz": ".gz",
		".gitignore":     NoExt,
		"dir/.env.local": ".local",
		"Makefile":       NoExt,
		"weird.":         NoExt,
		"dir.d/noext":    NoExt,
		"ünï cödé.Dat":   ".dat",
	}
	for in, want := range exts {
		if got := Ext(in); got != want {
			t.Errorf("Ext(%q) = %q, want %q", in, got, want)
		}
	}
	dirs := map[string]string{"a/b/c.txt": "a/b", "c.txt": RootDir, "dir with space/x": "dir with space"}
	for in, want := range dirs {
		if got := Dir(in); got != want {
			t.Errorf("Dir(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAggregatorTopN(t *testing.T) {
	blobs := []Blob{
		{"o1", "a", 100, 10},
		{"o2", "b", 50, 60},
		{"o3", "a", 300, 30},
		{"o4", "c.txt", 5, 5},
		{"o5", "d/e.txt", 70, 60},
	}
	a := NewAggregator(SortDisk, 3)
	for _, b := range blobs {
		a.Add(b)
	}
	var got []string
	for _, b := range a.TopBlobs() {
		got = append(got, b.OID)
	}
	if !reflect.DeepEqual(got, []string{"o2", "o5", "o3"}) { // tie 60/60 broken by path
		t.Errorf("disk top: %v", got)
	}
	s := NewAggregator(SortSize, 2)
	for _, b := range blobs {
		s.Add(b)
	}
	got = nil
	for _, b := range s.TopBlobs() {
		got = append(got, b.OID)
	}
	if !reflect.DeepEqual(got, []string{"o3", "o1"}) {
		t.Errorf("size top: %v", got)
	}
	paths := a.Paths()
	if paths[0].Key != "b" || paths[1].Key != "d/e.txt" || paths[2] != (Group{Key: "a", Blobs: 2, Size: 400, Disk: 40}) {
		t.Errorf("paths: %+v", paths)
	}
	if a.Total != (Group{Blobs: 5, Size: 525, Disk: 165}) {
		t.Errorf("total: %+v", a.Total)
	}
	if e := a.Exts(); e[0].Key != NoExt || e[0].Blobs != 3 {
		t.Errorf("exts: %+v", e)
	}
	if d := a.Dirs(); d[0].Key != RootDir || d[0].Blobs != 4 {
		t.Errorf("dirs: %+v", d)
	}
	if z := NewAggregator(SortDisk, 0); true {
		z.Add(blobs[0])
		if len(z.TopBlobs()) != 0 {
			t.Error("n=0 should keep no blobs")
		}
	}
}

func TestIntroEarlier(t *testing.T) {
	var in Intro
	if !in.Earlier("b", 10) {
		t.Error("empty should accept")
	}
	in = Intro{"b", 10}
	if in.Earlier("a", 11) || !in.Earlier("c", 9) || !in.Earlier("a", 10) || in.Earlier("c", 10) {
		t.Error("ordering")
	}
}

func TestBuildHistory(t *testing.T) {
	jan := int64(1705312800) // 2024-01-15
	apr := int64(1712145600) // 2024-04-03
	sizes := map[string][2]int64{"a": {100, 10}, "b": {200, 20}, "c": {300, 30}, "d": {7, 7}}
	first := map[string]int64{"a": jan, "b": jan, "c": apr, "zzz": apr}
	months, un := BuildHistory(sizes, first)
	want := []Month{
		{"2024-01", 2, 300, 30},
		{"2024-02", 0, 0, 0},
		{"2024-03", 0, 0, 0},
		{"2024-04", 1, 300, 30},
	}
	if !reflect.DeepEqual(months, want) {
		t.Errorf("got %+v", months)
	}
	if un != (Group{Blobs: 1, Size: 7, Disk: 7}) {
		t.Errorf("unattributed %+v", un)
	}
	if m, _ := BuildHistory(map[string][2]int64{}, nil); m != nil {
		t.Error("expected nil")
	}
	// Year boundary.
	dec := int64(1703980800) // 2023-12-31 00:00 UTC
	m, _ := BuildHistory(map[string][2]int64{"x": {1, 1}, "y": {1, 1}}, map[string]int64{"x": dec, "y": jan})
	if len(m) != 2 || m[0].Month != "2023-12" || m[1].Month != "2024-01" {
		t.Errorf("year boundary %+v", m)
	}
}

func TestShellQuote(t *testing.T) {
	tests := map[string]string{
		"big.bin":             "big.bin",
		"dir/sub-dir/a_b.txt": "dir/sub-dir/a_b.txt",
		"with space.txt":      "'with space.txt'",
		"it's.txt":            `'it'\''s.txt'`,
		"ünï.txt":             "'ünï.txt'",
		"$(rm -rf).txt":       "'$(rm -rf).txt'",
		"-dash":               "'-dash'",
		"":                    "''",
	}
	for in, want := range tests {
		if got := ShellQuote(in); got != want {
			t.Errorf("ShellQuote(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestBuildFix(t *testing.T) {
	if BuildFix(nil) != nil {
		t.Error("nil groups should give nil fix")
	}
	f := BuildFix([]Group{{Key: "db/dump.sql", Disk: 5}, {Key: "backup 1.zip", Disk: 7}})
	if f.FilterRepo != "git filter-repo --invert-paths --path db/dump.sql --path 'backup 1.zip'" {
		t.Errorf("filter-repo %q", f.FilterRepo)
	}
	if !reflect.DeepEqual(f.BFG, []string{"bfg --delete-files '{backup 1.zip,dump.sql}'"}) {
		t.Errorf("bfg %q", f.BFG)
	}
	if f.Bytes != 12 {
		t.Errorf("bytes %d", f.Bytes)
	}
	one := BuildFix([]Group{{Key: "a/x.bin"}})
	if !reflect.DeepEqual(one.BFG, []string{"bfg --delete-files x.bin"}) {
		t.Errorf("single bfg %q", one.BFG)
	}
	glob := BuildFix([]Group{{Key: "a,b.bin"}, {Key: "c.bin"}})
	if !reflect.DeepEqual(glob.BFG, []string{"bfg --delete-files a,b.bin", "bfg --delete-files c.bin"}) {
		t.Errorf("glob-unsafe bfg %q", glob.BFG)
	}
}

func TestHumanBytes(t *testing.T) {
	tests := map[int64]string{
		0:                      "0 B",
		1023:                   "1023 B",
		1024:                   "1.0 KiB",
		1536:                   "1.5 KiB",
		10 * 1024:              "10 KiB",
		1048575:                "1.0 MiB",
		486 * 1024 * 1024:      "486 MiB",
		5 * 1024 * 1024 * 1024: "5.0 GiB",
	}
	for in, want := range tests {
		if got := HumanBytes(in); got != want {
			t.Errorf("HumanBytes(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestThousands(t *testing.T) {
	tests := map[int64]string{0: "0", 999: "999", 1000: "1,000", 12408: "12,408", 1234567: "1,234,567", -1234: "-1,234"}
	for in, want := range tests {
		if got := Thousands(in); got != want {
			t.Errorf("Thousands(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestRevListEmitErrorStops(t *testing.T) {
	calls := 0
	err := RevListToBatchInput(bytes.NewReader([]byte("a\nb\n")), false, func([]byte) error {
		calls++
		return errStop
	})
	if err != errStop || calls != 1 {
		t.Errorf("err=%v calls=%d", err, calls)
	}
}

type stopErr struct{}

func (stopErr) Error() string { return "stop" }

var errStop = stopErr{}
