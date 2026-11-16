//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd

package xcodec

import (
	"golang.org/x/sys/unix"
	"os"
)

func lockVolume(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
}
