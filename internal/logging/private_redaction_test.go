package logging

import (
	"bytes"
	"mime/multipart"
	"strings"
	"testing"
)

func TestPrivateRPLogRedaction(t *testing.T) {
	t.Setenv("CPA_PRIVATE_RP", "1")
	for _, key := range []string{"Authorization", "Cookie", "Set-Cookie", "X-Api-Key", "X-Goog-Api-Key", "X-Access-Token"} {
		if got := RedactHeaderValue(key, "known-secret"); got != redactedValue {
			t.Errorf("header %s: got %q", key, got)
		}
	}
	if got := RedactHeaderValue("Content-Type", "application/json"); got != "application/json" {
		t.Fatalf("non-secret header changed: %q", got)
	}
	url := RedactURL("https://user:password@example.test/v1?access_token=known-secret&x-api-key=other-secret&model=gemini")
	if strings.Contains(url, "known-secret") || strings.Contains(url, "other-secret") || strings.Contains(url, "password") || !strings.Contains(url, "model=gemini") {
		t.Fatalf("unsafe URL: %s", url)
	}
	body := []byte(`{"messages":[{"content":"ordinary prompt"}], "auth":{"access_token":"known-secret","refresh_token":"another-secret"},"model":"gemini"}`)
	got := string(RedactCredentialFields(body))
	if strings.Contains(got, "known-secret") || strings.Contains(got, "another-secret") || !strings.Contains(got, `"content":"ordinary prompt"`) {
		t.Fatalf("unexpected redaction: %s", got)
	}
	if string(body) != `{"messages":[{"content":"ordinary prompt"}], "auth":{"access_token":"known-secret","refresh_token":"another-secret"},"model":"gemini"}` {
		t.Fatal("redaction mutated the original request")
	}
	extra := string(RedactCredentialFields([]byte(`{"session_token":"SESSION_SECRET","private_key":"PRIVATE_SECRET","key":"KEY_SECRET","content":"kept"}`)))
	if strings.Contains(extra, "SESSION_SECRET") || strings.Contains(extra, "PRIVATE_SECRET") || strings.Contains(extra, "KEY_SECRET") || !strings.Contains(extra, "kept") {
		t.Fatalf("additional credential field leaked: %s", extra)
	}
}

func TestPrivateRPMultipartAndFormRedaction(t *testing.T) {
	t.Setenv("CPA_PRIVATE_RP", "1")
	var original bytes.Buffer
	writer := multipart.NewWriter(&original)
	if err := writer.WriteField("access_token", "FORM_SECRET"); err != nil {
		t.Fatal(err)
	}
	part, err := writer.CreateFormFile("file", "image.bin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte{0, 1, 2, 255, 3}); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	redacted := RedactPrivateBody(original.Bytes(), writer.FormDataContentType())
	if bytes.Contains(redacted, []byte("FORM_SECRET")) || !bytes.Contains(redacted, []byte{0, 1, 2, 255, 3}) {
		t.Fatal("multipart credentials were leaked or media was changed")
	}
	form := string(RedactPrivateBody([]byte("client_secret=FORM_SECRET&message=hello"), "application/x-www-form-urlencoded"))
	if strings.Contains(form, "FORM_SECRET") || !strings.Contains(form, "message=hello") {
		t.Fatal("form fields were not redacted correctly")
	}
	malformed := string(RedactPrivateBody([]byte(`{"api_key":"SECRET"`), "application/json"))
	if strings.Contains(malformed, "SECRET") {
		t.Fatal("malformed JSON leaked a credential")
	}
}
