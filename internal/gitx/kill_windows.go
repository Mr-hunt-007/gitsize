//go:build windows

package gitx

import (
	"os/exec"
	"strconv"
)

// setCancel makes cancellation kill the whole process tree. On Windows the
// git.exe found on PATH is usually Git for Windows' launcher (cmd\git.exe),
// which runs the real git as a child; killing only the launcher would leave
// that child running and holding the repository directory open.
func setCancel(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		kill := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid))
		_ = kill.Run()
		return cmd.Process.Kill()
	}
}
