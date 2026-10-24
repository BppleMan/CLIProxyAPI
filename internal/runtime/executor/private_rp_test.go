package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/antigravity/openai/chat-completions"
	auth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	exec "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	tr "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestPrivateRPWireAndIsolation(t *testing.T) {
	t.Setenv("CPA_PRIVATE_RP", "1")
	bodies := make(chan []byte, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		bodies <- raw
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("auth changed")
		}
		response := `{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"OK"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":1}}}`
		if strings.Contains(r.URL.Path, "streamGenerateContent") {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "data: "+response+"\n\n")
		} else {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, response)
		}
	}))
	defer srv.Close()
	e := NewAntigravityExecutor(&config.Config{})
	a := &auth.Auth{ID: "rp-test", Metadata: map[string]any{"access_token": "test-token", "expired": time.Now().Add(time.Hour).Format(time.RFC3339), "project_id": "test-project"}, Attributes: map[string]string{"base_url": srv.URL}}
	var previousSession string
	for _, stream := range []bool{false, true} {
		body := []byte(`{"model":"gemini-3.1-pro","messages":[{"role":"system","content":"RP_ONLY"},{"role":"user","content":"edited history only"}],"max_tokens":333,"temperature":0.7,"top_p":0.8,"top_k":30,"stop":["END"]}`)
		req := exec.Request{Model: "gemini-3.1-pro", Payload: body}
		opts := exec.Options{SourceFormat: tr.FormatOpenAI, Stream: stream}
		if stream {
			r, err := e.ExecuteStream(context.Background(), a, req, opts)
			if err != nil {
				t.Fatal(err)
			}
			for c := range r.Chunks {
				if c.Err != nil {
					t.Fatal(c.Err)
				}
			}
		} else {
			if _, err := e.Execute(context.Background(), a, req, opts); err != nil {
				t.Fatal(err)
			}
		}
		raw := <-bodies
		if gjson.GetBytes(raw, "request.systemInstruction.parts.0.text").String() != "RP_ONLY" {
			t.Fatal("system changed")
		}
		if gjson.GetBytes(raw, "request.contents.#").Int() != 1 {
			t.Fatal("hidden history")
		}
		if gjson.GetBytes(raw, "request.generationConfig.maxOutputTokens").Int() != 333 {
			t.Fatal("token limit lost")
		}
		if gjson.GetBytes(raw, "request.generationConfig.stopSequences.0").String() != "END" {
			t.Fatal("stop lost")
		}
		current := gjson.GetBytes(raw, "request.sessionId").String()
		if current == "" || current == previousSession {
			t.Fatal("shared session")
		}
		previousSession = current
	}
}
