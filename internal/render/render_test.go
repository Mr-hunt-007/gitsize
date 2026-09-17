package render

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Mr-hunt-007/gitsize/internal/analyze"
	"github.com/Mr-hunt-007/gitsize/internal/scan"
)

func sample() *scan.Report {
	return &scan.Report{
		Schema:     scan.SchemaVersion,
		Repository: scan.Repository{Name: "myapp"},
		Storage:    scan.Storage{GitDirBytes: 486 << 20, PackedBytes: 471 << 20, LooseBytes: 15 << 20, Objects: 12408},
		HeadTree:   &scan.HeadTree{Files: 1204, Bytes: 38 << 20},
		Reachable:  scan.Reachable{Blobs: 9000, BlobBytes: 5 << 30, BlobDisk: 470 << 20},
		Options:    scan.Options{Largest: 10, Sort: "disk", By: scan.ByBlob, History: true},
		Blobs: []scan.BlobEntry{
			{Path: "database.sql", OID: "a1b2c3d4e5", Size: 190 << 20, Disk: 182 << 20, Status: scan.StatusDeleted,
				Introduced: &scan.Introduced{Commit: "a1b2c3d4e5f6", Date: "2024-03-02"}},
			{Path: "evil\x1b[31mname", OID: "ff", Size: 1, Disk: 1, Status: scan.StatusInHead},
		},
		History: &scan.History{Months: []analyze.Month{{Month: "2024-01", Blobs: 2, Disk: 100}, {Month: "2024-02"}, {Month: "2024-03", Blobs: 1, Disk: 50}}},
		Fix:     analyze.BuildFix([]analyze.Group{{Key: "database.sql", Disk: 182 << 20}}),
		Notes:   []string{"a note"},
	}
}

func TestText(t *testing.T) {
	var b bytes.Buffer
	if err := Text(&b, sample(), false); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{
		"Repository: myapp",
		".git         486 MiB  (packed 471 MiB, loose 15 MiB, 12,408 objects)",
		"Checkout      38 MiB  (files in HEAD: 1,204)",
		"Largest blobs (all history, by on-disk size):",
		"182 MiB    190 MiB  deleted      a1b2c3d 2024-03-02  database.sql",
		`"evil\x1b[31mname"`,
		"2024-01  ########################################",
		"0 B  0 blobs",
		"50 B  1 blob\n",
		"2024-03  ####################                ",
		"How to fix:",
		"WARNING: both commands below rewrite history.",
		"git filter-repo --invert-paths --path database.sql",
		"bfg --delete-files database.sql",
		"  - a note",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "\x1b") {
		t.Error("escape sequence leaked with color off")
	}
	if strings.ContainsRune(out, '\u2014') {
		t.Error("em dash in output")
	}
}

func TestTextColor(t *testing.T) {
	var b bytes.Buffer
	_ = Text(&b, sample(), true)
	if !strings.Contains(b.String(), "\x1b[31mdeleted") {
		t.Error("expected red deleted status")
	}
}

func TestTextNoFixSection(t *testing.T) {
	r := sample()
	r.Fix = nil
	var b bytes.Buffer
	_ = Text(&b, r, false)
	if strings.Contains(b.String(), "How to fix") || strings.Contains(b.String(), "filter-repo") {
		t.Error("fix section printed without deleted files")
	}
}

func TestTextModes(t *testing.T) {
	r := sample()
	r.Options.By = scan.ByPath
	r.Blobs = nil
	r.Paths = []scan.PathEntry{{Path: "a b.bin", Versions: 3, Size: 3000, Disk: 2048, Status: scan.StatusInHead}}
	var b bytes.Buffer
	_ = Text(&b, r, false)
	if !strings.Contains(b.String(), "Largest paths") || !strings.Contains(b.String(), "2.0 KiB") || !strings.Contains(b.String(), "a b.bin") {
		t.Errorf("path mode:\n%s", b.String())
	}
	r.Options.By = scan.ByDir
	r.Options.Sort = "size"
	r.Dirs = []scan.GroupEntry{{Key: "(root)", Blobs: 4, Size: 10, Disk: 5}}
	b.Reset()
	_ = Text(&b, r, false)
	if !strings.Contains(b.String(), "Directories") || !strings.Contains(b.String(), "uncompressed size") {
		t.Errorf("dir mode:\n%s", b.String())
	}
	r.Repository.Empty = true
	r.HeadTree = nil
	r.History = nil
	b.Reset()
	_ = Text(&b, r, false)
	if strings.Contains(b.String(), "Directories") || strings.Contains(b.String(), "History") {
		t.Errorf("empty repo:\n%s", b.String())
	}
}

func TestJSONShape(t *testing.T) {
	var b bytes.Buffer
	if err := JSON(&b, sample()); err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"schema", "repository", "storage", "head_tree", "reachable", "options", "largest_blobs", "history", "lfs", "fix", "notes", "count_objects"} {
		if _, ok := m[k]; !ok {
			t.Errorf("missing key %q", k)
		}
	}
	if _, ok := m["largest_paths"]; ok {
		t.Error("largest_paths should be omitted in blob mode")
	}
	blob := m["largest_blobs"].([]any)[0].(map[string]any)
	if blob["status"] != "deleted" || blob["disk"].(float64) != float64(182<<20) {
		t.Errorf("blob: %v", blob)
	}
}

func TestDisplayPath(t *testing.T) {
	tests := map[string]string{
		"plain/file.txt": "plain/file.txt",
		"ünï cödé 文件":    "ünï cödé 文件",
		"new\nline":      `"new\nline"`,
		"esc\x1b[2J":     `"esc\x1b[2J"`,
		"bad\xffutf8":    `"bad\xffutf8"`,
	}
	for in, want := range tests {
		if got := DisplayPath(in); got != want {
			t.Errorf("DisplayPath(%q) = %s, want %s", in, got, want)
		}
	}
}
