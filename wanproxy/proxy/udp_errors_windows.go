package proxy

import (
	"errors"
	"syscall"

	"golang.org/x/sys/windows"
)

// On Windows, syscall.ECONNRESET and other POSIX errno names are invented
// values. Network I/O returns native Winsock codes, which must be matched
// separately even when wrapped by net.OpError or os.SyscallError.
func platformUDPError(err error) bool {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return false
	}
	switch errno {
	case windows.WSAEMSGSIZE, windows.WSAENOBUFS, windows.WSAECONNREFUSED, windows.WSAECONNRESET, windows.WSAENETUNREACH, windows.WSAEHOSTUNREACH, windows.WSAENETRESET, windows.WSAENETDOWN, windows.WSAEHOSTDOWN:
		return true
	}
	return false
}
