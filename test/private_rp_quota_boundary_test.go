package test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/openai"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestPrivateRPQuotaHTTPBoundary(t *testing.T) {
	for _, mode := range []string{"nonstream", "stream-http-429", "stream-error-before-output", "stream-error-after-output"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("CPA_PRIVATE_RP", "1")
			fixture, err := os.ReadFile("fixtures/antigravity-quota/quota-exhausted.json")
			if err != nil {
				t.Fatal(err)
			}
			var mu sync.Mutex
			var generations []string
			var quotaCalls int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				account := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
				if r.URL.Path == "/v1internal:retrieveUserQuotaSummary" {
					mu.Lock()
					quotaCalls++
					mu.Unlock()
					_, _ = w.Write([]byte(`{"groups":[{"buckets":[{"bucketId":"gemini-five-hour","remainingFraction":0,"resetTime":"2099-01-01T00:00:00Z"}]}]}`))
					return
				}
				if r.URL.Path != "/v1internal:generateContent" && r.URL.Path != "/v1internal:streamGenerateContent" {
					t.Errorf("unexpected route %s", r.URL.Path)
					w.WriteHeader(404)
					return
				}
				body, _ := io.ReadAll(r.Body)
				if !strings.Contains(string(body), "synthetic opening") {
					t.Error("history was not forwarded")
				}
				if r.Header.Get("X-RP-Chat-ID") != "" {
					t.Error("frontend routing header leaked upstream")
				}
				mu.Lock()
				generations = append(generations, account)
				mu.Unlock()
				if account == "synthetic-a" {
					if mode == "stream-error-before-output" || mode == "stream-error-after-output" {
						w.Header().Set("Content-Type", "text/event-stream")
						if mode == "stream-error-after-output" {
							_, _ = io.WriteString(w, "data: {\"response\":{\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"partial-a\"}]}}]}}\n\n")
							w.(http.Flusher).Flush()
						}
						_, _ = fmt.Fprintf(w, "data: %s\n\n", strings.ReplaceAll(string(fixture), "\n", ""))
						return
					}
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(429)
					_, _ = w.Write(fixture)
					return
				}
				payload := `{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"b-success"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":1,"totalTokenCount":3}}}`
				if mode == "nonstream" {
					_, _ = io.WriteString(w, payload)
				} else {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
				}
			}))
			defer server.Close()
			selector := coreauth.NewSessionAffinitySelectorWithConfig(coreauth.SessionAffinityConfig{Fallback: &coreauth.FillFirstSelector{}, TTL: time.Hour, RebindOn: coreauth.SessionAffinityRebindOnQuota})
			manager := coreauth.NewManager(nil, selector, nil)
			defer manager.StopAutoRefresh()
			manager.SetRetryConfig(3, time.Second, 3)
			manager.RegisterExecutor(executor.NewAntigravityExecutor(&config.Config{}))
			model := "gemini-3-flash-rp-quota-" + mode
			for i, account := range []string{"synthetic-a", "synthetic-b"} {
				id := model + account
				_, err := manager.Register(context.Background(), &coreauth.Auth{ID: id, Provider: "antigravity", Status: coreauth.StatusActive, Attributes: map[string]string{"base_url": server.URL, "priority": fmt.Sprint(10 - i)}, Metadata: map[string]any{"access_token": account, "project_id": "synthetic-project", "expired": time.Now().Add(time.Hour).Format(time.RFC3339)}})
				if err != nil {
					t.Fatal(err)
				}
				registry.GetGlobalRegistry().RegisterClient(id, "antigravity", []*registry.ModelInfo{{ID: model}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
			}
			handler := openai.NewOpenAIAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{Streaming: sdkconfig.StreamingConfig{BootstrapRetries: 3}}, manager))
			router := gin.New()
			router.Use(func(c *gin.Context) {
				c.Set("userApiKey", "synthetic-client-key")
				c.Set("accessProvider", "config-api-key")
				c.Next()
			})
			router.POST("/v1/chat/completions", handler.ChatCompletions)
			send := func() *httptest.ResponseRecorder {
				body := fmt.Sprintf(`{"model":%q,"stream":%t,"messages":[{"role":"system","content":"synthetic system"},{"role":"user","content":"synthetic opening"}]}`, model, mode != "nonstream")
				req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
				// Match the typed immutable identity installed by AuthMiddleware.
				// A raw Gin context alone is recycled after stream completion.
				ctx := coreusage.WithAPIKey(req.Context(), "synthetic-client-key")
				ctx = coreusage.WithAccessProvider(ctx, "config-api-key")
				ctx = coreusage.WithIsNativeKey(ctx, true)
				req = req.WithContext(ctx)
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("X-RP-Client", "openwebui")
				req.Header.Set("X-RP-User-ID", "synthetic-user")
				req.Header.Set("X-RP-Chat-ID", "synthetic-chat")
				recorder := httptest.NewRecorder()
				router.ServeHTTP(recorder, req)
				return recorder
			}
			first := send()
			if mode == "stream-error-after-output" {
				if first.Code != 200 || !strings.Contains(first.Body.String(), "partial-a") || !strings.Contains(first.Body.String(), "429") {
					t.Fatalf("partial stream status=%d body=%s", first.Code, first.Body.String())
				}
			} else if first.Code != 429 {
				t.Fatalf("first status=%d body=%s", first.Code, first.Body.String())
			}
			mu.Lock()
			firstCalls := len(generations)
			mu.Unlock()
			if firstCalls != 1 {
				t.Fatalf("first request made %d generation attempts", firstCalls)
			}
			second := send()
			if second.Code != 200 || !strings.Contains(second.Body.String(), "b-success") {
				t.Fatalf("second status=%d body=%s", second.Code, second.Body.String())
			}
			// Exhaust the other account too: no model substitution or hidden
			// generation is allowed when the entire credential pool is blocked.
			delay := time.Hour
			manager.MarkResult(context.Background(), coreauth.Result{AuthID: model + "synthetic-b", Provider: "antigravity", Model: model, RetryAfter: &delay, Error: &coreauth.Error{HTTPStatus: 429, Message: string(fixture)}})
			third := send()
			if third.Code != 429 {
				t.Fatalf("empty pool status=%d body=%s", third.Code, third.Body.String())
			}
			mu.Lock()
			defer mu.Unlock()
			if len(generations) != 2 || generations[1] != "synthetic-b" {
				t.Fatalf("generation accounts=%v", generations)
			}
			if quotaCalls > 2 {
				t.Fatalf("duplicate quota probes=%d", quotaCalls)
			}
		})
	}
}
