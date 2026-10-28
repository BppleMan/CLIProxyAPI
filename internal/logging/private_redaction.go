package logging

import (
	"bytes"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/textproto"
	"net/url"
	"sort"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/rpmode"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	"github.com/tidwall/gjson"
)

const redactedValue = "[REDACTED]"

// RedactHeaderValue removes complete credential values from private RP logs.
func RedactHeaderValue(key, value string) string {
	if !rpmode.Enabled() {
		return util.MaskSensitiveHeaderValue(key, value)
	}
	lower := strings.ToLower(strings.TrimSpace(key))
	if strings.Contains(lower, "authorization") || strings.Contains(lower, "cookie") ||
		strings.Contains(lower, "api-key") || strings.Contains(lower, "api_key") ||
		strings.Contains(lower, "apikey") || strings.Contains(lower, "token") ||
		strings.Contains(lower, "secret") || lower == "authentication" || lower == "x-auth" || lower == "x-goog-api-client-key" {
		return redactedValue
	}
	return value
}

// RedactPrivateBody handles credential fields before any request body is spooled.
func RedactPrivateBody(body []byte, contentType string) []byte {
	if !rpmode.Enabled() || len(body) == 0 {
		return body
	}
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil && contentType != "" {
		ReportPrivateCaptureFailure()
		return []byte("[invalid content type; request body omitted]")
	}
	switch mediaType {
	case "text/event-stream":
		var redactor PrivateFrameRedactor
		var output bytes.Buffer
		emit := func(p []byte) { _, _ = output.Write(p) }
		redactor.Feed(body, emit)
		redactor.Finish(emit)
		return output.Bytes()
	case "application/x-www-form-urlencoded":
		values, err := url.ParseQuery(string(body))
		if err != nil {
			ReportPrivateCaptureFailure()
			return []byte("[invalid form body omitted]")
		}
		for key := range values {
			if sensitiveCredentialName(key) {
				values[key] = []string{redactedValue}
			}
		}
		return []byte(values.Encode())
	case "multipart/form-data":
		redacted, err := redactMultipartBody(body, params["boundary"])
		if err != nil {
			ReportPrivateCaptureFailure()
			return []byte("[invalid multipart body omitted]")
		}
		return redacted
	default:
		trimmed := bytes.TrimSpace(body)
		looksLikeJSON := mediaType == "" && len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[')
		if (mediaType == "application/json" || strings.HasSuffix(mediaType, "+json") || looksLikeJSON) && !gjson.ValidBytes(body) {
			ReportPrivateCaptureFailure()
			return []byte("[invalid JSON body omitted]")
		}
		return RedactCredentialFields(body)
	}
}

func redactMultipartBody(body []byte, boundary string) ([]byte, error) {
	if boundary == "" {
		return nil, fmt.Errorf("missing multipart boundary")
	}
	reader := multipart.NewReader(bytes.NewReader(body), boundary)
	var output bytes.Buffer
	writer := multipart.NewWriter(&output)
	if err := writer.SetBoundary(boundary); err != nil {
		return nil, err
	}
	for {
		part, err := reader.NextRawPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		headers := make(textproto.MIMEHeader, len(part.Header))
		for key, values := range part.Header {
			for _, value := range values {
				headers.Add(key, RedactHeaderValue(key, value))
			}
		}
		target, err := writer.CreatePart(headers)
		if err != nil {
			return nil, err
		}
		if sensitiveCredentialName(part.FormName()) {
			if _, err := io.WriteString(target, redactedValue); err != nil {
				return nil, err
			}
		} else if _, err := io.Copy(target, part); err != nil {
			return nil, err
		}
		if err := part.Close(); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func sensitiveCredentialName(key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	key = strings.ReplaceAll(strings.ReplaceAll(key, "-", ""), "_", "")
	if strings.Contains(key, "apikey") || strings.HasSuffix(key, "accesstoken") || strings.HasSuffix(key, "refreshtoken") {
		return true
	}
	switch key {
	case "key", "apikey", "token", "accesstoken", "refreshtoken", "idtoken",
		"oauthtoken", "oauthaccesstoken", "clientsecret", "authorization",
		"password", "secret", "credential", "credentials", "cookie", "setcookie":
		return true
	default:
		return false
	}
}

// RedactURL removes user info and credential query parameters from private RP logs.
func RedactURL(raw string) string {
	if !rpmode.Enabled() || raw == "" {
		return raw
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "[invalid URL redacted]"
	}
	parsed.User = nil
	parsed.Fragment = ""
	values, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		parsed.RawQuery = "[redacted]"
		return parsed.String()
	}
	for key := range values {
		lower := strings.ToLower(strings.TrimSuffix(key, "[]"))
		if sensitiveCredentialName(lower) || lower == "value" || lower == "code" || strings.Contains(lower, "api-key") || strings.Contains(lower, "api_key") || strings.Contains(lower, "token") || strings.Contains(lower, "secret") {
			values[key] = []string{redactedValue}
		}
	}
	parsed.RawQuery = values.Encode()
	return parsed.String()
}

type jsonRedaction struct{ start, end int }

// RedactCredentialFields masks credential fields without rewriting other JSON.
// Plain text supplied by the user remains unchanged.
func RedactCredentialFields(body []byte) []byte {
	if !rpmode.Enabled() || !gjson.ValidBytes(body) {
		return body
	}
	var edits []jsonRedaction
	var visit func(gjson.Result)
	visit = func(value gjson.Result) {
		value.ForEach(func(key, child gjson.Result) bool {
			if key.Type == gjson.String && sensitiveCredentialName(key.String()) {
				if child.Index >= 0 && child.Index+len(child.Raw) <= len(body) {
					edits = append(edits, jsonRedaction{child.Index, child.Index + len(child.Raw)})
				}
				return true
			}
			if child.IsObject() || child.IsArray() {
				visit(child)
			}
			return true
		})
	}
	visit(gjson.ParseBytes(body))
	if len(edits) == 0 {
		return body
	}
	sort.Slice(edits, func(i, j int) bool { return edits[i].start > edits[j].start })
	result := append([]byte(nil), body...)
	for _, edit := range edits {
		result = append(append(result[:edit.start:edit.start], []byte(`"[REDACTED]"`)...), result[edit.end:]...)
	}
	return result
}
