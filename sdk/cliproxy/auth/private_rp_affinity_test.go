package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestPrivateRPFrontendAffinityIsolation(t *testing.T) {
	t.Setenv("CPA_PRIVATE_RP", "1")
	const provider, model = "rp-isolation", "same-model"
	m := NewManager(nil, nil, nil)
	s := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{Fallback: &RoundRobinSelector{}, TTL: time.Hour, RebindOn: SessionAffinityRebindOnQuota})
	defer s.Stop()
	m.SetSelector(s)
	m.RegisterExecutor(schedulerTestExecutor{provider: provider})
	for _, id := range []string{"rp-account-a", "rp-account-b"} {
		_, err := m.Register(WithSkipPersist(context.Background()), &Auth{ID: id, Provider: provider, Status: StatusActive})
		if err != nil {
			t.Fatal(err)
		}
		registry.GetGlobalRegistry().RegisterClient(id, provider, []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	}
	makeOpts := func(client, user, chat string) cliproxyexecutor.Options {
		h := http.Header{}
		h.Set("X-RP-Client", client)
		h.Set("X-RP-User-ID", user)
		h.Set("X-RP-Chat-ID", chat)
		return cliproxyexecutor.Options{Headers: h, OriginalRequest: []byte(`{"messages":[{"role":"system","content":"same character"},{"role":"user","content":"same opening"}]}`)}
	}
	opts := []cliproxyexecutor.Options{makeOpts("openwebui", "u1", "chat-a"), makeOpts("openwebui", "u1", "chat-b"), makeOpts("librechat", "u1", "chat-a"), makeOpts("librechat", "u2", "chat-a")}
	identities := map[string]bool{}
	bindings := make([]string, len(opts))
	for i, o := range opts {
		id := ExtractSessionID(o.Headers, o.OriginalRequest, nil)
		if id == "" || identities[id] {
			t.Fatal("frontend/user/chat identity collision")
		}
		identities[id] = true
		a, _, err := m.pickNext(context.Background(), provider, model, o, nil)
		if err != nil {
			t.Fatal(err)
		}
		bindings[i] = a.ID
	}
	if bindings[0] == bindings[1] {
		t.Fatal("independent chats incorrectly reused the first binding")
	}
	for round := 0; round < 3; round++ {
		for i, o := range opts {
			a, _, err := m.pickNext(context.Background(), provider, model, o, nil)
			if err != nil || a.ID != bindings[i] {
				t.Fatalf("chat %d lost affinity: %v", i, err)
			}
		}
	}
}
