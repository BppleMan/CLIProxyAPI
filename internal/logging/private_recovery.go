package logging

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var privateRecoveryMu sync.RWMutex
var privateRecoveryDone <-chan struct{}

// StartPrivateLogRecovery does not delay serving inference when storage hangs.
// Background log writers wait until recovery finishes to avoid touching active files.
func StartPrivateLogRecovery(dir string) {
	done := make(chan struct{})
	privateRecoveryMu.Lock()
	privateRecoveryDone = done
	privateRecoveryMu.Unlock()
	go func() {
		defer close(done)
		if err := RecoverPrivateLogOrphans(dir); err != nil {
			ReportPrivateCaptureFailure()
		}
	}()
	go func() {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			ReportPrivateCaptureFailure()
		}
	}()
}

func waitPrivateLogRecovery() {
	privateRecoveryMu.RLock()
	done := privateRecoveryDone
	privateRecoveryMu.RUnlock()
	if done != nil {
		<-done
	}
}

// RecoverPrivateLogOrphans runs before serving requests in a single-writer process.
func RecoverPrivateLogOrphans(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		path := filepath.Join(dir, name)
		switch {
		case entry.Type().IsRegular() && strings.HasPrefix(name, ".request-active-") && strings.HasSuffix(name, ".tmp"):
			ReportPrivateCaptureFailure()
			file, errOpen := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0600)
			if errOpen != nil {
				return errOpen
			}
			_, errWrite := file.WriteString("\n=== LOG CAPTURE INCOMPLETE: PROCESS RESTARTED ===\n")
			errClose := file.Close()
			if errWrite != nil {
				return errWrite
			}
			if errClose != nil {
				return errClose
			}
			recovered := filepath.Join(dir, "recovered-"+strings.TrimSuffix(strings.TrimPrefix(name, ".request-active-"), ".tmp")+".log")
			if errLink := os.Link(path, recovered); errLink != nil {
				return errLink
			}
			if errRemove := os.Remove(path); errRemove != nil {
				return errRemove
			}
		case entry.IsDir() && strings.HasPrefix(name, "request-log-parts-"):
			ReportPrivateCaptureFailure()
			if errRemove := os.RemoveAll(path); errRemove != nil {
				return errRemove
			}
		case entry.Type().IsRegular() && (strings.HasPrefix(name, "request-body-") || strings.HasPrefix(name, "response-body-")) && strings.HasSuffix(name, ".tmp"):
			ReportPrivateCaptureFailure()
			if errRemove := os.Remove(path); errRemove != nil {
				return errRemove
			}
		}
	}
	return nil
}
