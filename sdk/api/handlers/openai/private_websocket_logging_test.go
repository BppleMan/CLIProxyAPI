package openai

import (
	"bytes"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	requestlogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
)

func TestPrivateWebsocketTimelineRedactsCredentials(t *testing.T) {
	t.Setenv("CPA_PRIVATE_RP", "1")
	t.Setenv("CPA_PRIVATE_RP_REQUEST_LOG", "1")
	source, err := requestlogging.NewFileBodySourceInDir(t.TempDir(), "timeline")
	if err != nil {
		t.Fatal(err)
	}
	timeline := newWebsocketTimelineLog(true, source)
	timeline.BeginRequest()
	timeline.Append("request", []byte(`{"content":"hello","api_key":"REQUEST_SECRET"}`), time.Unix(0, 0))
	timeline.Append("response", []byte(`{"content":"reply","access_token":"RESPONSE_SECRET"}`), time.Unix(0, 0))
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	timeline.SetContext(c)
	var result bytes.Buffer
	if _, err := source.WriteTo(&result); err != nil {
		t.Fatal(err)
	}
	if err := source.Cleanup(); err != nil {
		t.Fatal(err)
	}
	text := result.String()
	if strings.Contains(text, "REQUEST_SECRET") || strings.Contains(text, "RESPONSE_SECRET") || !strings.Contains(text, "hello") || !strings.Contains(text, "reply") {
		t.Fatalf("unexpected websocket log: %s", text)
	}
}
