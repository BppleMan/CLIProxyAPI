package logging

import (
	"fmt"
	"io"
	"sync"
	"sync/atomic"
)

const privateSourceChunkBytes = 64 * 1024
const privateSourceQueueBytes = 96 * 1024 * 1024

var privateSourceQueuedBytes atomic.Int64

// privateAsyncSource isolates request execution from an unresponsive log disk.
type privateAsyncSource struct {
	queue   chan []byte
	done    chan struct{}
	mu      sync.Mutex
	partMu  sync.Mutex
	closed  bool
	hasData atomic.Bool
	dropped atomic.Bool
	failed  atomic.Bool
	disk    *FileBodySource
	err     error
}

func newPrivateAsyncSource(baseDir, prefix string) *privateAsyncSource {
	s := &privateAsyncSource{queue: make(chan []byte, privateSourceQueueBytes/privateSourceChunkBytes), done: make(chan struct{})}
	go func() {
		defer close(s.done)
		waitPrivateLogRecovery()
		disk, err := newDiskFileBodySource(baseDir, prefix)
		if err != nil {
			s.failed.Store(true)
			ReportPrivateCaptureFailure()
		}
		for chunk := range s.queue {
			if err == nil {
				err = disk.AppendBytes(chunk)
				if err != nil {
					s.failed.Store(true)
					ReportPrivateCaptureFailure()
				}
			}
			privateSourceQueuedBytes.Add(-int64(len(chunk)))
		}
		s.disk, s.err = disk, err
	}()
	return s
}

func reservePrivateSourceBytes(n int) bool {
	for {
		current := privateSourceQueuedBytes.Load()
		if current+int64(n) > privateSourceQueueBytes {
			return false
		}
		if privateSourceQueuedBytes.CompareAndSwap(current, current+int64(n)) {
			return true
		}
	}
}

func (s *privateAsyncSource) append(data []byte) error {
	for len(data) > 0 {
		n := len(data)
		if n > privateSourceChunkBytes {
			n = privateSourceChunkBytes
		}
		if s.failed.Load() || !reservePrivateSourceBytes(n) {
			s.dropped.Store(true)
			ReportPrivateCaptureDrop()
			return fmt.Errorf("private log queue unavailable")
		}
		chunk := append([]byte(nil), data[:n]...)
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			privateSourceQueuedBytes.Add(-int64(n))
			return fmt.Errorf("private log source closed")
		}
		select {
		case s.queue <- chunk:
			s.hasData.Store(true)
			s.mu.Unlock()
		default:
			s.mu.Unlock()
			privateSourceQueuedBytes.Add(-int64(n))
			s.dropped.Store(true)
			ReportPrivateCaptureDrop()
			return fmt.Errorf("private log queue full")
		}
		data = data[n:]
	}
	return nil
}

func (s *privateAsyncSource) finish() (*FileBodySource, error) {
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		close(s.queue)
	}
	s.mu.Unlock()
	<-s.done
	return s.disk, s.err
}

func (s *privateAsyncSource) writeTo(w io.Writer) (int64, error) {
	disk, err := s.finish()
	if err != nil {
		return 0, err
	}
	if disk == nil {
		return 0, nil
	}
	n, err := disk.WriteTo(w)
	if err == nil && s.dropped.Load() {
		written, errMarker := io.WriteString(w, "\n=== LOG CAPTURE INCOMPLETE: LOG QUEUE FULL ===\n")
		n += int64(written)
		err = errMarker
	}
	return n, err
}
