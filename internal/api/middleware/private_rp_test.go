package middleware

import (
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
)

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
