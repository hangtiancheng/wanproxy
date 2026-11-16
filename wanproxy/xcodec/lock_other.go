//go:build !linux && !darwin && !dragonfly && !freebsd && !netbsd && !openbsd && !windows

package xcodec

import (
	"errors"
	"os"
)

func lockVolume(*os.File) error {
	return errors.New("XCDFS file locking is unsupported on this operating system")
}
