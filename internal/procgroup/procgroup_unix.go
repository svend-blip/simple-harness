//go:build !windows

package procgroup

import (
	"errors"
	"syscall"
)

// Attr makes the subprocess the leader of a new process group whose id
// equals its pid, so a signal to -pid reaches the whole tree and never
// the parent's group.
func Attr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true, Pgid: 0}
}

// Signal sends sig to the process group led by pid — i.e. to -pid,
// which is valid as long as ANY member is alive, even after the leader
// itself has exited and been reaped (a shell that backgrounded a child
// and returned, for instance). Resolving the group via Getpgid at
// signal time would fail with ESRCH in exactly that case and leave the
// survivors running. A group that is entirely gone is not an error.
func Signal(pid int, sig syscall.Signal) error {
	if err := syscall.Kill(-pid, sig); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}
