package analyze

import (
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var shellSafe = regexp.MustCompile(`^[A-Za-z0-9_./@%+=:,-]+$`)

// ShellQuote quotes s for a POSIX shell.
func ShellQuote(s string) string {
	if s != "" && shellSafe.MatchString(s) && !strings.HasPrefix(s, "-") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Fix holds the suggested (never executed) history-rewrite commands.
type Fix struct {
	Paths      []string `json:"paths"`
	Bytes      int64    `json:"disk_bytes"`
	FilterRepo string   `json:"filter_repo"`
	BFG        []string `json:"bfg"`
}

// BuildFix returns the rewrite commands for removing paths from all history,
// or nil when there is nothing to suggest.
func BuildFix(groups []Group) *Fix {
	if len(groups) == 0 {
		return nil
	}
	f := &Fix{}
	var b strings.Builder
	b.WriteString("git filter-repo --invert-paths")
	names := map[string]bool{}
	for _, g := range groups {
		f.Paths = append(f.Paths, g.Key)
		f.Bytes += g.Disk
		b.WriteString(" --path ")
		b.WriteString(ShellQuote(g.Key))
		names[path.Base(g.Key)] = true
	}
	f.FilterRepo = b.String()
	sorted := make([]string, 0, len(names))
	simple := true
	for n := range names {
		sorted = append(sorted, n)
		if strings.ContainsAny(n, "{},*?[]\\") {
			simple = false
		}
	}
	sort.Strings(sorted)
	switch {
	case len(sorted) == 1:
		f.BFG = []string{"bfg --delete-files " + ShellQuote(sorted[0])}
	case simple:
		// One run with a glob alternation instead of one rewrite per file.
		f.BFG = []string{"bfg --delete-files " + ShellQuote("{"+strings.Join(sorted, ",")+"}")}
	default:
		for _, n := range sorted {
			f.BFG = append(f.BFG, "bfg --delete-files "+ShellQuote(n))
		}
	}
	return f
}

// HumanBytes formats n with binary (IEC) units: "512 B", "9.8 KiB", "486 MiB".
func HumanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return strconv.FormatInt(n, 10) + " B"
	}
	units := []string{"KiB", "MiB", "GiB", "TiB", "PiB", "EiB"}
	v := float64(n) / unit
	i := 0
	for v >= unit && i < len(units)-1 {
		v /= unit
		i++
	}
	if v < 9.95 {
		return strconv.FormatFloat(v, 'f', 1, 64) + " " + units[i]
	}
	if v >= 1023.5 && i < len(units)-1 {
		return "1.0 " + units[i+1]
	}
	return strconv.FormatFloat(v, 'f', 0, 64) + " " + units[i]
}

// Thousands formats n with comma separators.
func Thousands(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}
