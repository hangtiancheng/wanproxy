package proxy

import (
	"context"
	"time"
)

var timeNow = time.Now

func waitNetworkRetry(ctx context.Context, backoff *time.Duration) bool {
	if *backoff == 0 {
		*backoff = time.Millisecond
	} else {
		*backoff = min((*backoff)*2, 100*time.Millisecond)
	}
	timer := time.NewTimer(*backoff)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
