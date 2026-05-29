//go:build !unix

package runner

import "os/exec"

// configureProcessGroup is a no-op on platforms without POSIX process groups.
func configureProcessGroup(cmd *exec.Cmd) {}

// terminate makes a best-effort kill of the direct child process only.
//
// TODO: terminate the whole process tree on these platforms (e.g. via Windows
// job objects) so that descendants are not orphaned.
func terminate(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
