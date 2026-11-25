package common

import (
	"os"
	"strings"
	"sync"
)

// APIDUO:local-image-async BEGIN

var (
	localImageAsyncOnce sync.Once
	localImageAsyncOn   bool
)

// LocalImageAsyncEnabled reports whether the gateway may execute a synchronous
// image upstream itself.
//
// On by default. When on, an asynchronous endpoint may select a synchronous
// channel: the submit only stores the request, and a worker replays it later.
// That costs a channel slot for the whole generation and keeps the request body
// in the task row, which is why an operator can still turn it off with
// LOCAL_IMAGE_ASYNC_ENABLED=false (or 0/off).
//
// Read once: every dispatch decision in a process must agree, and an env read on
// the hot path would be a measurable cost for a value that never changes.
func LocalImageAsyncEnabled() bool {
	localImageAsyncOnce.Do(func() {
		switch strings.ToLower(strings.TrimSpace(os.Getenv("LOCAL_IMAGE_ASYNC_ENABLED"))) {
		case "false", "0", "off":
			localImageAsyncOn = false
		default:
			localImageAsyncOn = true
		}
	})
	return localImageAsyncOn
}

// APIDUO:local-image-async END
