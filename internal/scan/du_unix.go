//go:build unix

package scan

import (
	"io/fs"
	"syscall"
)

// diskUsage returns the space a file occupies on disk, like du, which is what
// git count-objects also reports for loose objects on Unix.
func diskUsage(info fs.FileInfo) int64 {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return int64(st.Blocks) * 512
	}
	return info.Size()
}
