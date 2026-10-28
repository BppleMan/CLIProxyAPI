package logging

import (
	"io"
	"sync"
	"time"
)

// A limited number of workers prevents repeated configuration changes from
// accumulating unbounded goroutines on a permanently blocked filesystem.
var privateLogWorkers = make(chan struct{}, 4)

type privateAsyncLogWriter struct {
	mu     sync.Mutex
	closed bool
	queue  chan []byte
}

func newPrivateAsyncLogWriter(output io.WriteCloser, prepare func() error) *privateAsyncLogWriter {
	select {
	case privateLogWorkers <- struct{}{}:
	default:
		ReportPrivateCaptureFailure()
		return nil
	}
	w := &privateAsyncLogWriter{queue: make(chan []byte, 256)}
	go func() {
		defer func() { <-privateLogWorkers }()
		err := prepare()
		var retryAfter time.Time
		if err != nil {
			ReportPrivateCaptureFailure()
			retryAfter = time.Now().Add(5 * time.Second)
		}
		for item := range w.queue {
			if err != nil && !time.Now().Before(retryAfter) {
				err = prepare()
				if err != nil {
					ReportPrivateCaptureFailure()
					retryAfter = time.Now().Add(5 * time.Second)
				}
			}
			if err == nil {
				var written int
				written, err = output.Write(item)
				if err == nil && written != len(item) {
					err = io.ErrShortWrite
				}
				if err != nil {
					ReportPrivateCaptureFailure()
					retryAfter = time.Now().Add(5 * time.Second)
				}
			}
		}
		_ = output.Close()
	}()
	return w
}

func (w *privateAsyncLogWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed || len(p) > 64*1024 {
		ReportPrivateCaptureDrop()
		return len(p), nil
	}
	select {
	case w.queue <- append([]byte(nil), p...):
	default:
		ReportPrivateCaptureDrop()
	}
	return len(p), nil
}

func (w *privateAsyncLogWriter) stop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.closed {
		w.closed = true
		close(w.queue)
	}
}
