package rpmode

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// FrontendSession scopes affinity to a frontend, user and real conversation ID.
// It never derives identities from prompt text or per-message IDs.
func FrontendSession(headers http.Header) (string, bool, error) {
	if !Enabled() {
		return "", false, nil
	}
	keys := []string{"X-RP-Client", "X-RP-User-ID", "X-RP-Chat-ID"}
	present := false
	for _, key := range keys {
		present = present || len(headers.Values(key)) > 0
	}
	if !present {
		return "", false, nil
	}
	parts := make([]string, len(keys))
	for i, key := range keys {
		values := headers.Values(key)
		if len(values) != 1 {
			return "", true, fmt.Errorf("RP frontend requires one %s header", key)
		}
		value := strings.TrimSpace(values[0])
		lower := strings.ToLower(value)
		if value == "" || len(value) > 512 || strings.ContainsAny(value, "{}\r\n\t") || strings.Contains(lower, "%7b") || lower == "new" || lower == "undefined" || lower == "null" {
			return "", true, fmt.Errorf("RP frontend requires a resolved %s header", key)
		}
		parts[i] = value
	}
	if parts[0] != "openwebui" && parts[0] != "librechat" {
		return "", true, fmt.Errorf("unsupported RP frontend")
	}
	encoded, _ := json.Marshal(parts)
	digest := sha256.Sum256(encoded)
	return "rp:" + hex.EncodeToString(digest[:]), true, nil
}
