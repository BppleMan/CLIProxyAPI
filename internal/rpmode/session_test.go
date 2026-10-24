package rpmode

import (
	"net/http"
	"strings"
	"testing"
)

func TestFrontendSessionValidation(t *testing.T) {
	t.Setenv("CPA_PRIVATE_RP", "1")
	base := http.Header{}
	base.Set("X-RP-Client", "openwebui")
	base.Set("X-RP-User-ID", "user")
	base.Set("X-RP-Chat-ID", "chat")
	for _, value := range []string{"", "new", "null", "undefined", "{{CHAT_ID}}", "%7B%7BCHAT_ID%7D%7D", strings.Repeat("x", 513)} {
		h := base.Clone()
		h.Set("X-RP-Chat-ID", value)
		if _, present, err := FrontendSession(h); !present || err == nil {
			t.Errorf("accepted unresolved identity %q", value)
		}
	}
	h := base.Clone()
	h.Add("X-RP-Chat-ID", "other")
	if _, _, err := FrontendSession(h); err == nil {
		t.Fatal("accepted duplicate header")
	}
	if _, present, err := FrontendSession(nil); present || err != nil {
		t.Fatal("unmarked API client changed")
	}
	t.Setenv("CPA_PRIVATE_RP", "0")
	if _, present, err := FrontendSession(base); present || err != nil {
		t.Fatal("normal mode changed")
	}
}
