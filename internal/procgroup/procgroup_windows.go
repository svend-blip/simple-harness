//go:build windows

package procgroup

import (
	"os/exec"
	"strconv"
	"syscall"
)

// Attr puts the subprocess in a new process group (console control
// events stay off FlowRunner's group).
func Attr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

// Signal terminates pid's process tree. Windows has no graceful group
// signal, so SIGTERM and SIGKILL both end in taskkill /T /F; a tree that
// is already gone is not an error.
func Signal(pid int, _ syscall.Signal) error {
	out, err := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid)).CombinedOutput()
	if err == nil {
		return nil
	}
	if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 128 {
		return nil // "process not found": already gone
	}
	_ = out
	return err
}
