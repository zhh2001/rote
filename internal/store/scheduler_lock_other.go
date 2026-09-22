//go:build !darwin && !linux

package store

import (
	"fmt"
	"os"
)

func trySchedulerLock(*os.File) error {
	return fmt.Errorf("scheduler locking is supported only on Linux and macOS")
}
