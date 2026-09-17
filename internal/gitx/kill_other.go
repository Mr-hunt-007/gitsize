//go:build !windows

package gitx

import "os/exec"

// setCancel keeps exec's default: cancellation kills the git process, which
// is the real git rather than a launcher.
func setCancel(cmd *exec.Cmd) {}
