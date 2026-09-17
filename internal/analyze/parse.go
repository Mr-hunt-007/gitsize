// Package analyze holds the pure parsing and aggregation logic of gitsize.
// Nothing here runs processes or touches the filesystem.
package analyze

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// CountObjects is the parsed output of `git count-objects -v`, in bytes.
type CountObjects struct {
	LooseObjects  int64 `json:"loose_objects"`
	LooseBytes    int64 `json:"loose_bytes"`
	PackedObjects int64 `json:"packed_objects"`
	Packs         int64 `json:"packs"`
	PackedBytes   int64 `json:"packed_bytes"`
	PrunePackable int64 `json:"prune_packable"`
	Garbage       int64 `json:"garbage_files"`
	GarbageBytes  int64 `json:"garbage_bytes"`
}

// ParseCountObjects parses `git count-objects -v`. Sizes there are KiB.
func ParseCountObjects(s string) (CountObjects, error) {
	var c CountObjects
	seen := 0
	for _, line := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		n, err := strconv.ParseInt(strings.TrimSpace(val), 10, 64)
		if err != nil {
			return c, fmt.Errorf("count-objects: bad value for %q: %w", key, err)
		}
		seen++
		switch strings.TrimSpace(key) {
		case "count":
			c.LooseObjects = n
		case "size":
			c.LooseBytes = n * 1024
		case "in-pack":
			c.PackedObjects = n
		case "packs":
			c.Packs = n
		case "size-pack":
			c.PackedBytes = n * 1024
		case "prune-packable":
			c.PrunePackable = n
		case "garbage":
			c.Garbage = n
		case "size-garbage":
			c.GarbageBytes = n * 1024
		default:
			seen--
		}
	}
	if seen == 0 {
		return c, fmt.Errorf("count-objects: no recognised fields in output")
	}
	return c, nil
}

// Object is one record from `git cat-file --batch-check` with the format
// "%(objecttype) %(objectname) %(objectsize) %(objectsize:disk) %(rest)".
type Object struct {
	Type    string
	OID     string
	Size    int64
	Disk    int64
	Path    string
	Missing bool
}

// BatchCheckFormat is the format string gitsize passes to cat-file.
const BatchCheckFormat = "%(objecttype) %(objectname) %(objectsize) %(objectsize:disk) %(rest)"

// ParseBatchCheck parses one output record (without its terminator).
func ParseBatchCheck(rec string) (Object, error) {
	if strings.HasSuffix(rec, " missing") && !strings.Contains(strings.TrimSuffix(rec, " missing"), " ") {
		return Object{OID: strings.TrimSuffix(rec, " missing"), Missing: true}, nil
	}
	if strings.HasSuffix(rec, " ambiguous") && !strings.Contains(strings.TrimSuffix(rec, " ambiguous"), " ") {
		return Object{}, fmt.Errorf("cat-file: ambiguous object %q", strings.TrimSuffix(rec, " ambiguous"))
	}
	typ, rest, ok1 := strings.Cut(rec, " ")
	oid, rest, ok2 := strings.Cut(rest, " ")
	size, rest, ok3 := strings.Cut(rest, " ")
	disk, path, _ := strings.Cut(rest, " ")
	if !ok1 || !ok2 || !ok3 {
		return Object{}, fmt.Errorf("cat-file: malformed record %q", rec)
	}
	o := Object{Type: typ, OID: oid, Path: path}
	var err error
	if o.Size, err = strconv.ParseInt(size, 10, 64); err != nil {
		return Object{}, fmt.Errorf("cat-file: bad size in %q", rec)
	}
	if o.Disk, err = strconv.ParseInt(disk, 10, 64); err != nil {
		return Object{}, fmt.Errorf("cat-file: bad disk size in %q", rec)
	}
	return o, nil
}

// RevListToBatchInput converts `git rev-list --objects` output into cat-file
// input records ("<oid> <path>" or "<oid>"), calling emit for each.
//
// With nul=true the input is the `rev-list -z --objects` format
// ("<oid>\0path=<path>\0") and emitted records may contain any byte except
// NUL. With nul=false the input is newline separated and a path containing a
// newline is truncated by git itself.
func RevListToBatchInput(r io.Reader, nul bool, emit func(rec []byte) error) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	if !nul {
		for sc.Scan() {
			line := bytes.TrimSuffix(sc.Bytes(), []byte("\r"))
			if len(line) == 0 {
				continue
			}
			if err := emit(line); err != nil {
				return err
			}
		}
		return sc.Err()
	}
	sc.Split(splitNUL)
	var pending []byte
	flush := func() error {
		if pending == nil {
			return nil
		}
		err := emit(pending)
		pending = nil
		return err
	}
	for sc.Scan() {
		tok := sc.Bytes()
		if len(tok) == 0 {
			continue
		}
		if bytes.HasPrefix(tok, []byte("path=")) {
			if pending == nil {
				return fmt.Errorf("rev-list: path token without object")
			}
			pending = append(append(pending, ' '), tok[len("path="):]...)
			continue
		}
		if bytes.IndexByte(tok, '=') >= 0 {
			continue // other key=value attributes (e.g. missing=yes)
		}
		if err := flush(); err != nil {
			return err
		}
		pending = append([]byte{}, tok...)
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return flush()
}

func splitNUL(data []byte, atEOF bool) (int, []byte, error) {
	if i := bytes.IndexByte(data, 0); i >= 0 {
		return i + 1, data[:i], nil
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

// ScanRecords calls fn for each record terminated by sep.
func ScanRecords(r io.Reader, sep byte, fn func(rec string) error) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	if sep == 0 {
		sc.Split(splitNUL)
	}
	for sc.Scan() {
		rec := sc.Text()
		if sep != 0 {
			rec = strings.TrimSuffix(rec, "\r")
		}
		if rec == "" {
			continue
		}
		if err := fn(rec); err != nil {
			return err
		}
	}
	return sc.Err()
}

// TreeEntry is one record of `git ls-tree -r -l -z`.
type TreeEntry struct {
	Mode string
	Type string
	OID  string
	Size int64 // -1 when git prints "-" (submodules) or "BAD" (object not present locally)
	Path string
}

// ParseLsTree parses one NUL-terminated `git ls-tree -r -l -z` record:
// "<mode> SP <type> SP <oid> SP+ <size> TAB <path>".
func ParseLsTree(rec string) (TreeEntry, error) {
	meta, path, ok := strings.Cut(rec, "\t")
	if !ok {
		return TreeEntry{}, fmt.Errorf("ls-tree: malformed record %q", rec)
	}
	f := strings.Fields(meta)
	if len(f) != 4 {
		return TreeEntry{}, fmt.Errorf("ls-tree: malformed record %q", rec)
	}
	e := TreeEntry{Mode: f[0], Type: f[1], OID: f[2], Size: -1, Path: path}
	if f[3] != "-" && f[3] != "BAD" {
		n, err := strconv.ParseInt(f[3], 10, 64)
		if err != nil {
			return TreeEntry{}, fmt.Errorf("ls-tree: bad size in %q", rec)
		}
		e.Size = n
	}
	return e, nil
}

// LogCommit is a commit header from the raw log pass.
type LogCommit struct {
	Hash string
	Time int64 // committer time, unix seconds
}

// LogChange is one raw diff entry.
type LogChange struct {
	DstMode string
	DstOID  string
	Status  string
	Path    string
}

// LogFormat is the --format passed to git log for ParseRawLog.
const LogFormat = "%H %ct"

// ParseRawLog parses the output of
//
//	git log --all --format='%H %ct' --raw --no-abbrev -z --no-renames
//
// calling fn for each change with the commit it belongs to.
func ParseRawLog(r io.Reader, fn func(LogCommit, LogChange) error) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	sc.Split(splitNUL)
	var cur LogCommit
	var meta []string
	expectPath := false
	for sc.Scan() {
		tok := sc.Text()
		if expectPath {
			expectPath = false
			if len(meta) < 5 {
				continue
			}
			ch := LogChange{DstMode: meta[1], DstOID: meta[3], Status: meta[4], Path: tok}
			if err := fn(cur, ch); err != nil {
				return err
			}
			continue
		}
		tok = strings.TrimLeft(tok, "\n")
		if tok == "" {
			continue
		}
		if tok[0] == ':' {
			meta = strings.Fields(tok[1:])
			if len(meta) < 5 {
				return fmt.Errorf("log: malformed raw entry %q", tok)
			}
			// Renames/copies carry two paths; --no-renames should prevent them.
			expectPath = true
			continue
		}
		hash, ts, ok := strings.Cut(tok, " ")
		if !ok {
			return fmt.Errorf("log: malformed commit header %q", tok)
		}
		t, err := strconv.ParseInt(strings.TrimSpace(ts), 10, 64)
		if err != nil {
			return fmt.Errorf("log: bad timestamp in %q", tok)
		}
		cur = LogCommit{Hash: hash, Time: t}
	}
	return sc.Err()
}

// IsZeroOID reports whether oid is all zeros (a deleted side in raw diffs).
func IsZeroOID(oid string) bool {
	return strings.Trim(oid, "0") == ""
}

// LFSPointer is a parsed Git LFS pointer file.
type LFSPointer struct {
	OID  string
	Size int64
}

const lfsVersionPrefix = "version https://git-lfs.github.com/spec/"

// ParseLFSPointer reports whether content is a Git LFS pointer and parses it.
// Pointers are small UTF-8 text files starting with the spec version line.
func ParseLFSPointer(content []byte) (LFSPointer, bool) {
	if len(content) >= 1024 || !bytes.HasPrefix(content, []byte(lfsVersionPrefix)) {
		return LFSPointer{}, false
	}
	var p LFSPointer
	haveSize := false
	for _, line := range strings.Split(string(content), "\n") {
		k, v, ok := strings.Cut(strings.TrimSuffix(line, "\r"), " ")
		if !ok {
			continue
		}
		switch k {
		case "oid":
			p.OID = v
		case "size":
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil || n < 0 {
				return LFSPointer{}, false
			}
			p.Size = n
			haveSize = true
		}
	}
	if p.OID == "" || !haveSize {
		return LFSPointer{}, false
	}
	return p, true
}
