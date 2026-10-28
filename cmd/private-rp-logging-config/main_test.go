package main

import (
	"bytes"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestPreparePreservesCredentialsAndEnablesBoundedLogging(t *testing.T) {
	input := []byte(`management:
  secret-key: "$2a$10$01234567890123456789012345678901234567890123456789012"
  allow-remote: true
observability:
  logs:
    logging-to-file: false
    request-log: false
auth-dir: /data/auth
api-keys:
  - synthetic-client-key
`)
	out, err := prepare(input)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.ParseConfigBytes(out)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.LoggingToFile || !cfg.RequestLog || cfg.LogsMaxTotalSizeMB != 18432 || !cfg.RemoteManagement.DisableAutoUpdatePanel {
		t.Fatalf("unexpected effective log configuration")
	}
	if !bytes.Contains(out, []byte("synthetic-client-key")) || !cfg.RemoteManagement.AllowRemote {
		t.Fatal("unrelated configuration was lost")
	}
	again, err := prepare(out)
	if err != nil || !bytes.Equal(out, again) {
		t.Fatalf("preparation is not idempotent: %v", err)
	}
}
