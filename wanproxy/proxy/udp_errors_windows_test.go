package proxy

import (
	"net"
	"os"
	"testing"

	"golang.org/x/sys/windows"
)

func TestUDPNativeWinsockErrors(t *testing.T) {
	for _, errno := range []error{windows.WSAEMSGSIZE, windows.WSAECONNRESET, windows.WSAECONNREFUSED, windows.WSAENOBUFS, windows.WSAENETUNREACH, windows.WSAEHOSTUNREACH} {
		err := &net.OpError{Op: "read", Net: "udp", Err: &os.SyscallError{Syscall: "recvfrom", Err: errno}}
		if !recoverableUDPError(err) {
			t.Errorf("native packet error treated as fatal: %v", err)
		}
	}
	if recoverableUDPError(windows.WSAENOTSOCK) || recoverableUDPError(net.ErrClosed) {
		t.Fatal("invalid socket error treated as recoverable")
	}
}
