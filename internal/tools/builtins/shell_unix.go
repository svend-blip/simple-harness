//go:build !windows

package builtins

import "os/exec"

// shellCommand runs command through the POSIX shell.
func shellCommand(command string) *exec.Cmd {
	return exec.Command("sh", "-c", command)
}
