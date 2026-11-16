//go:build !windows

package proxy

func platformUDPError(error) bool { return false }
