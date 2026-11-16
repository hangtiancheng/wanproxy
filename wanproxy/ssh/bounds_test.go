package ssh

import (
	"net"
	"testing"
	"time"
)

func TestCloseCancelsAndJoinsHandshake(t *testing.T) {
	a, b := net.Pipe()
	defer b.Close()
	tr := NewTransport(a, Config{Role: ServerRole, HostKey: generateTestKey(t)})
	done := make(chan error, 1)
	go func() { done <- tr.Handshake() }()
	deadline := time.Now().Add(time.Second)
	for {
		tr.mu.Lock()
		started := tr.started
		tr.mu.Unlock()
		if started {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("handshake did not start")
		}
		time.Sleep(time.Millisecond)
	}
	closed := make(chan error, 1)
	go func() { closed <- tr.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not join blocked handshake")
	}
	if err := <-done; err == nil {
		t.Fatal("closed handshake succeeded")
	}
	if err := tr.Handshake(); err == nil {
		t.Fatal("restarted closed transport")
	}
}

func TestFingerprintValidation(t *testing.T) {
	for _, fp := range []string{"", "SHA1:abc", "SHA256:abc", "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=", "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAB"} {
		if err := ValidateHostKeyFingerprint(fp); err == nil {
			t.Fatalf("accepted invalid fingerprint %q", fp)
		}
	}
	if err := ValidateHostKeyFingerprint("SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"); err != nil {
		t.Fatal(err)
	}
}
