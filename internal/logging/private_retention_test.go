package logging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestPrivateRetentionCountsActiveFilesAndKeepsThem(t *testing.T) {
	t.Setenv("CPA_PRIVATE_RP", "1")
	t.Setenv("CPA_PRIVATE_RP_REQUEST_LOG", "1")
	dir := t.TempDir()
	old := filepath.Join(dir, "request-old.log")
	newer := filepath.Join(dir, "request-new.log")
	active := filepath.Join(dir, ".request-active.tmp")
	for path, data := range map[string]string{old: strings.Repeat("a", 100), newer: strings.Repeat("b", 100), active: strings.Repeat("c", 100)} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chtimes(old, time.Unix(1, 0), time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(newer, time.Unix(2, 0), time.Unix(2, 0)); err != nil {
		t.Fatal(err)
	}
	deleted, err := enforceLogDirSizeLimit(dir, 210, "")
	if err != nil || deleted != 1 {
		t.Fatalf("unexpected cleanup result: %d %v", deleted, err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("oldest completed log survived cleanup")
	}
	for _, path := range []string{newer, active} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("retained file was removed: %s", path)
		}
	}
}

func TestPrivateLogDirectoryDoesNotFallbackIntoAuthVolume(t *testing.T) {
	t.Setenv("CPA_PRIVATE_RP", "1")
	t.Setenv("CPA_PRIVATE_RP_REQUEST_LOG", "1")
	t.Setenv("WRITABLE_PATH", "")
	t.Setenv("writable_path", "")
	t.Chdir(t.TempDir())
	cfg := &config.Config{AuthDir: t.TempDir()}
	if got := ResolveLogDirectory(cfg); got != "logs" {
		t.Fatalf("private log directory unexpectedly used auth storage: %q", got)
	}
}

func TestPrivateStreamDropIsVisible(t *testing.T) {
	t.Setenv("CPA_PRIVATE_RP", "1")
	t.Setenv("CPA_PRIVATE_RP_REQUEST_LOG", "1")
	writer := &FileStreamingLogWriter{chunkChan: make(chan []byte, 1)}
	writer.chunkChan <- []byte("occupied")
	_, before, _ := PrivateCaptureStatus()
	writer.WriteChunkAsync([]byte("dropped"))
	_, after, _ := PrivateCaptureStatus()
	if !writer.incomplete.Load() || after != before+1 {
		t.Fatal("dropped chunk was not reported")
	}
}

func TestPrivateRecoveryMarksPartialLogsAndRemovesOrphanParts(t *testing.T) {
	dir := t.TempDir()
	active := filepath.Join(dir, ".request-active-123.tmp")
	if err := os.WriteFile(active, []byte("partial content"), 0600); err != nil {
		t.Fatal(err)
	}
	parts := filepath.Join(dir, "request-log-parts-api-response-123")
	if err := os.Mkdir(parts, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parts, "part.tmp"), []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	before, _, _ := PrivateCaptureStatus()
	if err := RecoverPrivateLogOrphans(dir); err != nil {
		t.Fatal(err)
	}
	after, _, _ := PrivateCaptureStatus()
	data, err := os.ReadFile(filepath.Join(dir, "recovered-123.log"))
	if err != nil || !strings.Contains(string(data), "partial content") || !strings.Contains(string(data), "INCOMPLETE") || after < before+2 {
		t.Fatalf("partial recovery was not marked: %v", err)
	}
	if _, err := os.Stat(parts); !os.IsNotExist(err) {
		t.Fatal("orphaned parts survived recovery")
	}
}
