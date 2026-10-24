package session

import (
	"net/http"
	"strings"
	"testing"
)

func TestPrivateRPExplicitIdentityOverridesContentFallback(t *testing.T) {
	t.Setenv("CPA_PRIVATE_RP", "1")
	h := http.Header{}
	h.Set("X-RP-Client", "librechat")
	h.Set("X-RP-User-ID", "u1")
	h.Set("X-RP-Chat-ID", "c1")
	h.Set("X-Claude-Code-Session-Id", "shared-unrelated-id")
	first, ok := ExtractSessionInfo(h, []byte(`{"session_id":"shared","messages":[{"role":"user","content":"same opening"}]}`), nil)
	if !ok || !strings.HasPrefix(first.SessionID, "rp:") {
		t.Fatal("RP identity was overridden")
	}
	next, ok := ExtractSessionInfo(h, []byte(`{"session_id":"different","messages":[{"role":"user","content":"edited opening"}]}`), nil)
	if !ok || first.SessionID != next.SessionID {
		t.Fatal("editing messages changed the conversation identity")
	}
	h.Set("X-RP-Chat-ID", "c2")
	other, ok := ExtractSessionInfo(h, nil, nil)
	if !ok || other.SessionID == first.SessionID {
		t.Fatal("different chats merged")
	}
}
