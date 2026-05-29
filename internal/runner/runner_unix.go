//go:build unix

package runner

import (
	"os"
	"os/exec"
	"syscall"
)

// configureProcessGroup places the child in a new process group so that all of
// its descendants can be signaled together.
func configureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// terminate kills the child's entire process group, reaping any descendants it
// spawned. Because configureProcessGroup makes the child a group leader, its
// process-group id equals its pid; signaling the negative pid reaches every
// member. The pid is used directly rather than looked up via Getpgid so that
// the group is still reachable even after the direct child has been reaped (the
// child may exit while a backgrounded descendant keeps the group alive).
func terminate(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		if err == syscall.ESRCH {
			return os.ErrProcessDone
		}
		return err
	}
	return nil
}
