package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestPrivateRPQuotaOnlyUsesAccountTransport(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls++
		if req.URL.Path != "/v1internal:retrieveUserQuotaSummary" || req.Method != http.MethodPost {
			t.Errorf("unexpected quota route: %s %s", req.Method, req.URL.Path)
		}
		if req.Header.Get("Authorization") != "Bearer synthetic-token" {
			t.Error("missing test authorization")
		}
		if req.Header.Get("User-Agent") == "" {
			t.Error("missing existing Antigravity user agent")
		}
		body, _ := io.ReadAll(req.Body)
		if !strings.Contains(string(body), `"project":"synthetic-project"`) {
			t.Error("missing project")
		}
		_, _ = w.Write([]byte(`{"groups":[{"buckets":[{"bucketId":"gemini","remainingFraction":0.25,"resetTime":"2026-10-25T10:00:00Z"}]}]}`))
	}))
	defer server.Close()
	e := NewAntigravityExecutor(&config.Config{})
	auth := &coreauth.Auth{ID: "synthetic-quota-auth", Provider: "antigravity", Attributes: map[string]string{"base_url": server.URL}, Metadata: map[string]any{"access_token": "synthetic-token", "project_id": "synthetic-project", "expired": time.Now().Add(time.Hour).Format(time.RFC3339)}}
	snapshot, err := e.RefreshRPQuota(context.Background(), auth)
	if err != nil || len(snapshot.Buckets) != 1 {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
	if calls != 1 {
		t.Fatalf("calls=%d, want quota-only single request", calls)
	}
}

func TestPrivateRPQuotaErrorContracts(t *testing.T) {
	for _, test := range []struct {
		file  string
		kind  antigravity429Category
		delay time.Duration
	}{
		{"quota-exhausted", antigravity429QuotaExhausted, time.Hour},
		{"rate-limited", antigravity429RateLimited, 500 * time.Millisecond},
		{"capacity-exhausted", antigravity429SoftRateLimit, 30 * time.Second},
	} {
		t.Run(test.file, func(t *testing.T) {
			body, err := os.ReadFile("../../../test/fixtures/antigravity-quota/" + test.file + ".json")
			if err != nil {
				t.Fatal(err)
			}
			if got := classifyAntigravity429(body); got != test.kind {
				t.Fatalf("kind=%s want=%s", got, test.kind)
			}
			status := newAntigravityStatusErr(429, body)
			if status.retryAfter == nil || *status.retryAfter != test.delay {
				t.Fatalf("retry delay=%v", status.retryAfter)
			}
		})
	}
}
