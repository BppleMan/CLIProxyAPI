package logging

import (
	"sync/atomic"
	"time"
)

var privateCaptureFailures atomic.Uint64
var privateCaptureDrops atomic.Uint64
var privateLastCaptureFailure atomic.Int64

// ReportPrivateCaptureFailure records an incomplete capture without logging payloads.
func ReportPrivateCaptureFailure() {
	privateCaptureFailures.Add(1)
	privateLastCaptureFailure.Store(time.Now().Unix())
}

// ReportPrivateCaptureDrop records a dropped stream chunk.
func ReportPrivateCaptureDrop() {
	privateCaptureDrops.Add(1)
	privateLastCaptureFailure.Store(time.Now().Unix())
}

// PrivateCaptureStatus exposes process-local capture errors to the management UI.
func PrivateCaptureStatus() (failures, drops uint64, lastFailure int64) {
	return privateCaptureFailures.Load(), privateCaptureDrops.Load(), privateLastCaptureFailure.Load()
}
