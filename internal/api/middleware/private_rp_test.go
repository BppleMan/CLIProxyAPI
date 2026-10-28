package middleware

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
)

type blockedPrivateLogger struct {
	*testRequestLogger
	entered chan struct{}
	release chan struct{}
}

func (l *blockedPrivateLogger) LogRequest(string, string, map[string][]string, []byte, int, map[string][]string, []byte, []byte, []byte, []byte, []byte, []*interfaces.ErrorMessage, string, time.Time, time.Time) error {
	l.entered <- struct{}{}
	<-l.release
	return nil
}

func TestPrivateRPSaturatedLogWorkersDoNotBlockInference(t *testing.T) {
	t.Setenv("CPA_PRIVATE_RP", "1")
	t.Setenv("CPA_PRIVATE_RP_REQUEST_LOG", "1")
	logger := &blockedPrivateLogger{testRequestLogger: &testRequestLogger{enabled: true}, entered: make(chan struct{}, cap(privateCaptureSlots)), release: make(chan struct{})}
	r := gin.New()
	r.Use(RequestLoggingMiddleware(logger))
	r.POST("/v1/chat/completions", func(c *gin.Context) {
		ctx := context.WithValue(c.Request.Context(), "gin", c)
		cfg := &config.Config{}
		cfg.RequestLog = true
		helps.RecordAPIRequest(ctx, cfg, helps.UpstreamRequestLog{Body: []byte(`{"message":"hello"}`)})
		if c.GetBool(logging.PrivateCaptureDisabledContextKey) {
			if _, exists := c.Get("API_UPSTREAM_ATTEMPTS"); exists {
				t.Error("disabled capture still retained upstream data")
			}
		}
		c.String(200, "response-ok")
	})
	defer func() { close(logger.release); privateCaptureWait.Wait() }()
	for i := 0; i < cap(privateCaptureSlots); i++ {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{}`)))
		if w.Code != 200 || w.Body.String() != "response-ok" {
			t.Fatal("inference response was blocked or changed")
		}
		select {
		case <-logger.entered:
		case <-time.After(3 * time.Second):
			t.Fatal("background log did not start")
		}
	}
	before, _, _ := logging.PrivateCaptureStatus()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{}`)))
	after, _, _ := logging.PrivateCaptureStatus()
	if w.Code != 200 || w.Body.String() != "response-ok" || after <= before {
		t.Fatal("saturated logging blocked inference or failed to report a skipped record")
	}
}

func TestPrivateStreamingDropPreservesWriteStringResult(t *testing.T) {
	t.Setenv("CPA_PRIVATE_RP", "1")
	t.Setenv("CPA_PRIVATE_RP_REQUEST_LOG", "1")
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	w := NewResponseWriterWrapper(c.Writer, &testRequestLogger{enabled: true}, &RequestInfo{})
	w.isStreaming = true
	w.streamInitialized = true
	w.streamWriter = &testStreamingLogWriter{}
	w.chunkChannel = make(chan []byte)
	payload := strings.Repeat("a", 128*1024)
	n, err := w.WriteString(payload)
	if err != nil || n != len(payload) || recorder.Body.String() != payload {
		t.Fatalf("log queue changed WriteString result: n=%d err=%v", n, err)
	}
}

func TestPrivateRPNoErrorBodyFiles(t *testing.T) {
	t.Setenv("CPA_PRIVATE_RP", "1")
	dir := t.TempDir()
	l := logging.NewFileRequestLogger(true, dir, "", 10)
	r := gin.New()
	r.Use(RequestLoggingMiddleware(l))
	r.POST("/v1/chat/completions", func(c *gin.Context) { c.String(400, "SECRET_ERROR") })
	q := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"messages":[{"content":"SECRET_BODY"}]}`))
	q.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(httptest.NewRecorder(), q)
	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Fatal("request/error log created")
	}
}

func TestPrivateRPRequestLogPreservesInferenceAndRedactsCredentials(t *testing.T) {
	t.Setenv("CPA_PRIVATE_RP", "1")
	t.Setenv("CPA_PRIVATE_RP_REQUEST_LOG", "1")
	dir := t.TempDir()
	logger := logging.NewFileRequestLogger(true, dir, "", 10)
	r := gin.New()
	r.Use(logging.GinLogrusLogger(), RequestLoggingMiddleware(logger))
	body := `{"messages":[{"role":"system","content":"RP_SYSTEM"},{"role":"user","content":"RP_HISTORY"}],"image":"` + strings.Repeat("b", 3*1024*1024) + `","api_key":"BODY_SECRET"}`
	r.POST("/v1/chat/completions", func(c *gin.Context) {
		received, _ := io.ReadAll(c.Request.Body)
		if string(received) != body {
			t.Error("logging altered the upstream-facing client request")
		}
		ctx := context.WithValue(c.Request.Context(), "gin", c)
		cfg := &config.Config{}
		cfg.RequestLog = true
		helps.RecordAPIRequest(ctx, cfg, helps.UpstreamRequestLog{
			URL: "https://example.test/chat?access_token=URL_SECRET&model=gemini", Method: "POST",
			Headers: http.Header{"Authorization": {"Bearer UPSTREAM_SECRET"}, "Cookie": {"COOKIE_SECRET"}},
			Body:    []byte(`{"system":"UPSTREAM_SYSTEM","accessToken":"JSON_SECRET"}`), AuthType: "oauth", AuthValue: "UPSTREAM_SECRET",
		})
		helps.RecordAPIResponseMetadata(ctx, cfg, 200, http.Header{"Set-Cookie": {"SET_COOKIE_SECRET"}})
		helps.AppendAPIResponseChunk(ctx, cfg, []byte(`{"reply":"UPSTREAM_REPLY","access_token":"RESPONSE_SECRET"}`))
		c.Header("Set-Cookie", "FRONT_COOKIE_SECRET")
		c.Data(200, "application/json", []byte(`{"reply":"FRONT_REPLY","api_key":"FRONT_RESPONSE_SECRET"}`))
	})
	q := httptest.NewRequest("POST", "/v1/chat/completions?token=FRONT_URL_SECRET", strings.NewReader(body))
	q.Header.Set("Authorization", "Bearer FRONT_SECRET")
	q.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, q)
	privateCaptureWait.Wait()
	if w.Code != 200 || w.Body.String() != `{"reply":"FRONT_REPLY","api_key":"FRONT_RESPONSE_SECRET"}` {
		t.Fatalf("inference response changed: %d %s", w.Code, w.Body.String())
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.log"))
	if err != nil || len(files) != 1 {
		t.Fatalf("expected one log: %v %v", files, err)
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, secret := range []string{"BODY_SECRET", "URL_SECRET", "UPSTREAM_SECRET", "COOKIE_SECRET", "JSON_SECRET", "SET_COOKIE_SECRET", "FRONT_COOKIE_SECRET", "FRONT_URL_SECRET", "FRONT_SECRET", "RESPONSE_SECRET", "FRONT_RESPONSE_SECRET"} {
		if strings.Contains(text, secret) {
			t.Errorf("credential leaked: %s", secret)
		}
	}
	for _, value := range []string{"RP_SYSTEM", "RP_HISTORY", "UPSTREAM_SYSTEM", "UPSTREAM_REPLY", "FRONT_REPLY", strings.Repeat("b", 3*1024*1024)} {
		if !strings.Contains(text, value) {
			t.Errorf("missing logged content length %d", len(value))
		}
	}
	if info, err := os.Stat(files[0]); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("unexpected request log permissions: %v %v", info, err)
	}
}

func TestPrivateRPLogFailureDoesNotChangeResponse(t *testing.T) {
	t.Setenv("CPA_PRIVATE_RP", "1")
	t.Setenv("CPA_PRIVATE_RP_REQUEST_LOG", "1")
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	logger := logging.NewFileRequestLogger(true, blocked, "", 10)
	r := gin.New()
	r.Use(RequestLoggingMiddleware(logger))
	r.POST("/v1/chat/completions", func(c *gin.Context) { c.String(200, "response-ok") })
	before, _, _ := logging.PrivateCaptureStatus()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"message":"hello"}`)))
	privateCaptureWait.Wait()
	after, _, _ := logging.PrivateCaptureStatus()
	if w.Code != 200 || w.Body.String() != "response-ok" || after <= before {
		t.Fatalf("logging failure affected inference or was not reported: status=%d failures=%d/%d", w.Code, before, after)
	}
}

func TestPrivateRPStreamingLogCapturesFrontendAndUpstream(t *testing.T) {
	t.Setenv("CPA_PRIVATE_RP", "1")
	t.Setenv("CPA_PRIVATE_RP_REQUEST_LOG", "1")
	dir := t.TempDir()
	logger := logging.NewFileRequestLogger(true, dir, "", 10)
	r := gin.New()
	r.Use(logging.GinLogrusLogger(), RequestLoggingMiddleware(logger))
	r.POST("/v1/chat/completions", func(c *gin.Context) {
		ctx := context.WithValue(c.Request.Context(), "gin", c)
		cfg := &config.Config{}
		cfg.RequestLog = true
		helps.RecordAPIRequest(ctx, cfg, helps.UpstreamRequestLog{URL: "https://example.test", Body: []byte(`{"prompt":"UPSTREAM_PROMPT"}`)})
		helps.RecordAPIResponseMetadata(ctx, cfg, 200, nil)
		helps.AppendAPIResponseChunk(ctx, cfg, []byte(`data: {"text":"UPSTREAM_CHUNK","access_`))
		helps.AppendAPIResponseChunk(ctx, cfg, []byte("token\":\"UPSTREAM_STREAM_SECRET\"}\n\n"))
		c.Header("Content-Type", "text/event-stream")
		c.Writer.Flush()
		_, _ = c.Writer.Write([]byte(`data: {"text":"FRONT_CHUNK","api_`))
		_, _ = c.Writer.Write([]byte("key\":\"FRONT_STREAM_SECRET\"}\n\n"))
		c.Writer.Flush()
	})
	q := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"messages":[{"content":"FRONT_PROMPT"}],"stream":true}`))
	q.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, q)
	privateCaptureWait.Wait()
	if w.Code != 200 || !strings.Contains(w.Body.String(), "FRONT_CHUNK") {
		t.Fatalf("streaming response changed: %d %s", w.Code, w.Body.String())
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.log"))
	if err != nil || len(files) != 1 {
		t.Fatalf("expected one streaming log: %v %v", files, err)
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"FRONT_PROMPT", "UPSTREAM_PROMPT", "UPSTREAM_CHUNK", "FRONT_CHUNK"} {
		if !strings.Contains(string(data), value) {
			t.Errorf("missing %s in streaming log: %s", value, string(data))
		}
	}
	for _, secret := range []string{"UPSTREAM_STREAM_SECRET", "FRONT_STREAM_SECRET"} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("stream credential leaked: %s", secret)
		}
		if secret == "FRONT_STREAM_SECRET" && !strings.Contains(w.Body.String(), secret) {
			t.Fatal("logging changed the streamed inference response")
		}
	}
}

func TestPrivateRPRejectsMissingConversationBeforeHandler(t *testing.T) {
	t.Setenv("CPA_PRIVATE_RP", "1")
	r := gin.New()
	r.Use(PrivateRPSessionMiddleware())
	r.POST("/v1/chat/completions", func(c *gin.Context) { c.Status(204) })
	r.GET("/v1/models", func(c *gin.Context) { c.Status(204) })
	for _, tc := range []struct {
		method, path, chat string
		want               int
	}{
		{"POST", "/v1/chat/completions", "", 400},
		{"POST", "/v1/chat/completions", "{{CHAT_ID}}", 400},
		{"POST", "/v1/chat/completions", "chat-1", 204},
		{"GET", "/v1/models", "", 204},
	} {
		q := httptest.NewRequest(tc.method, tc.path, strings.NewReader(`{"messages":[{"role":"user","content":"PRIVATE_OPENING"}]}`))
		q.Header.Set("X-RP-Client", "openwebui")
		q.Header.Set("X-RP-User-ID", "user-1")
		q.Header.Set("X-RP-Chat-ID", tc.chat)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, q)
		if w.Code != tc.want {
			t.Fatalf("%s: got %d want %d", tc.path, w.Code, tc.want)
		}
		if strings.Contains(w.Body.String(), "PRIVATE_OPENING") {
			t.Fatal("error disclosed message body")
		}
	}
}
