//go:build windows

package builtins

import "os/exec"

// shellCommand prefers a POSIX sh when one is on PATH (Git for Windows
// ships one, and the governance texts the models follow are written for
// it); otherwise it falls back to cmd /C.
func shellCommand(command string) *exec.Cmd {
	if sh, err := exec.LookPath("sh"); err == nil {
		return exec.Command(sh, "-c", command)
	}
	return exec.Command("cmd", "/C", command)
}
