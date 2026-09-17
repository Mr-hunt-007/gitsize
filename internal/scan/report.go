// Package scan runs git against a repository and assembles the gitsize report.
package scan

import "github.com/Mr-hunt-007/gitsize/internal/analyze"

// SchemaVersion is bumped on any breaking change to the JSON shape.
const SchemaVersion = 1

// Report is the complete result, serialised as-is by --json.
type Report struct {
	Schema     int                  `json:"schema"`
	Repository Repository           `json:"repository"`
	Storage    Storage              `json:"storage"`
	HeadTree   *HeadTree            `json:"head_tree"`
	Reachable  Reachable            `json:"reachable"`
	Options    Options              `json:"options"`
	Blobs      []BlobEntry          `json:"largest_blobs,omitempty"`
	Paths      []PathEntry          `json:"largest_paths,omitempty"`
	Exts       []GroupEntry         `json:"by_ext,omitempty"`
	Dirs       []GroupEntry         `json:"by_dir,omitempty"`
	History    *History             `json:"history,omitempty"`
	LFS        *LFS                 `json:"lfs"`
	Fix        *analyze.Fix         `json:"fix"`
	Notes      []string             `json:"notes"`
	Counts     analyze.CountObjects `json:"count_objects"`

	// Available is not part of the JSON shape. It records how many rows
	// existed before Largest was applied, so callers can say what was cut.
	Available Available `json:"-"`
}

// Available counts rows before truncation to Largest.
type Available struct {
	// Rows is the number of entries for the selected By mode: blob versions,
	// distinct paths, extensions or directories.
	Rows int64
	// FixPaths is the number of deleted paths over FixThreshold.
	FixPaths int
}

// Repository describes what was scanned.
type Repository struct {
	Name         string `json:"name"`
	GitDir       string `json:"git_dir"`
	WorkTree     string `json:"work_tree,omitempty"`
	Bare         bool   `json:"bare"`
	Shallow      bool   `json:"shallow"`
	PartialClone bool   `json:"partial_clone"`
	Empty        bool   `json:"empty"`
	HeadResolves bool   `json:"head_resolves"`
	GitVersion   string `json:"git_version"`
}

// Storage is what the repository costs on disk.
type Storage struct {
	GitDirBytes   int64 `json:"git_dir_bytes"`
	PackedBytes   int64 `json:"packed_bytes"`
	LooseBytes    int64 `json:"loose_bytes"`
	Objects       int64 `json:"objects"`
	LFSCacheBytes int64 `json:"lfs_cache_bytes"`
}

// HeadTree is the size of the files in the HEAD commit (what a checkout holds).
type HeadTree struct {
	Files int64 `json:"files"`
	Bytes int64 `json:"bytes"`
	// SizesUnknown counts files whose blob is not present locally (partial
	// clones); their size is not included in Bytes.
	SizesUnknown int64 `json:"sizes_unknown"`
}

// Reachable totals every object reachable from any ref or HEAD.
type Reachable struct {
	Objects   int64 `json:"objects"`
	DiskBytes int64 `json:"disk_bytes"`
	Blobs     int64 `json:"blobs"`
	BlobBytes int64 `json:"blob_bytes"`
	BlobDisk  int64 `json:"blob_disk_bytes"`
	Missing   int64 `json:"missing"`
}

// Options echoes the options that shaped the report.
type Options struct {
	Largest int    `json:"largest"`
	Sort    string `json:"sort"`
	By      string `json:"by"`
	History bool   `json:"history"`
}

// Status values for blobs and paths, relative to HEAD.
const (
	StatusInHead     = "in HEAD"
	StatusOldVersion = "old version"
	StatusDeleted    = "deleted"
	StatusUnknown    = "unknown"
)

// Introduced is the earliest commit (by committer date) that added something.
type Introduced struct {
	Commit string `json:"commit"`
	Time   int64  `json:"time"`
	Date   string `json:"date"` // YYYY-MM-DD, UTC
}

// BlobEntry is one blob version.
type BlobEntry struct {
	Path       string      `json:"path"`
	OID        string      `json:"oid"`
	Size       int64       `json:"size"`
	Disk       int64       `json:"disk"`
	Status     string      `json:"status"`
	Introduced *Introduced `json:"introduced"`
	LFS        bool        `json:"lfs_pointer,omitempty"`
}

// PathEntry aggregates every blob version stored under one path.
type PathEntry struct {
	Path       string      `json:"path"`
	Versions   int64       `json:"versions"`
	Size       int64       `json:"size"`
	Disk       int64       `json:"disk"`
	Status     string      `json:"status"`
	Introduced *Introduced `json:"introduced"`
}

// GroupEntry aggregates blobs by extension or directory.
type GroupEntry struct {
	Key   string `json:"key"`
	Blobs int64  `json:"blobs"`
	Size  int64  `json:"size"`
	Disk  int64  `json:"disk"`
}

// History is repository growth by month of first commit.
type History struct {
	Months       []analyze.Month `json:"months"`
	Unattributed GroupEntry      `json:"unattributed"`
}

// LFS describes Git LFS pointer files found in history.
type LFS struct {
	PointerBlobs     int64 `json:"pointer_blobs"`
	PointersInHead   int64 `json:"pointers_in_head"`
	LFSObjects       int64 `json:"lfs_objects"`
	LFSContentBytes  int64 `json:"lfs_content_bytes"`
	AttributesInHead bool  `json:"attributes_in_head"`
}
