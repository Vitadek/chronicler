// Package activity tracks when the server last saw real HTTP traffic. It
// exists for pkg/grammarsweep's idle-triggered background check: the sweep
// must stay out of the way while the server is genuinely in use, and back
// off within one tick of activity resuming.
package activity

import (
	"sync/atomic"
	"time"
)

var lastRequestAtMs atomic.Int64

// Touch records that a request just happened. Call from a middleware on
// every request — cheap enough (one atomic store) not to need sampling.
func Touch() {
	lastRequestAtMs.Store(time.Now().UnixNano() / int64(time.Millisecond))
}

// MillisSinceLastRequest returns how long it's been since the last recorded
// request, or a very large number if none has ever been recorded (treated
// as "idle" by any reasonable threshold).
func MillisSinceLastRequest() int64 {
	last := lastRequestAtMs.Load()
	if last == 0 {
		return 1 << 62
	}
	return time.Now().UnixNano()/int64(time.Millisecond) - last
}
