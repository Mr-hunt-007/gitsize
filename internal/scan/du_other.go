//go:build !unix

package scan

import "io/fs"

// diskUsage returns the file size; block counts are not available here.
func diskUsage(info fs.FileInfo) int64 { return info.Size() }
