//go:build darwin || linux

package store

import (
	"errors"
	"os"
	"syscall"
)

func trySchedulerLock(file *os.File) error {
	for {
		err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		switch {
		case errors.Is(err, syscall.EINTR):
			continue
		case errors.Is(err, syscall.EWOULDBLOCK), errors.Is(err, syscall.EAGAIN):
			return ErrSchedulerRunning
		default:
			return err
		}
	}
}
