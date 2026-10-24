package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type rpQuotaExecutor struct {
	mockCustomErrorExecutor
	mu           sync.Mutex
	calls        []string
	streamMode   string
	probeStarted chan struct{}
	probeCalls   int
}

func (e *rpQuotaExecutor) Execute(_ context.Context, auth *Auth, req coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
	e.mu.Lock()
	e.calls = append(e.calls, auth.ID+":"+req.Model)
	e.mu.Unlock()
	if strings.HasSuffix(auth.ID, "-a") {
		delay := time.Hour
		return coreexecutor.Response{}, customStatusError{code: 429, msg: `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"QUOTA_EXHAUSTED"},{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"3600s"}]}}`, retryAfter: &delay}
	}
	return coreexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
}

func (e *rpQuotaExecutor) ExecuteStream(ctx context.Context, auth *Auth, req coreexecutor.Request, opts coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	resp, err := e.Execute(ctx, auth, req, opts)
	if err != nil && e.streamMode == "http" {
		return nil, err
	}
	ch := make(chan coreexecutor.StreamChunk, 2)
	if err != nil {
		if e.streamMode == "midstream" {
			ch <- coreexecutor.StreamChunk{Payload: []byte("already-delivered")}
		}
		ch <- coreexecutor.StreamChunk{Err: err}
	} else {
		ch <- coreexecutor.StreamChunk{Payload: resp.Payload}
	}
	close(ch)
	return &coreexecutor.StreamResult{Chunks: ch}, nil
}

func (e *rpQuotaExecutor) RefreshRPQuota(ctx context.Context, _ *Auth) (RPQuotaSnapshot, error) {
	e.mu.Lock()
	e.probeCalls++
	e.mu.Unlock()
	select {
	case e.probeStarted <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return RPQuotaSnapshot{}, ctx.Err()
}

func newRPQuotaManager(t *testing.T, mode string) (*Manager, *rpQuotaExecutor, string, string) {
	t.Helper()
	e := &rpQuotaExecutor{mockCustomErrorExecutor: mockCustomErrorExecutor{identifier: "antigravity"}, streamMode: mode, probeStarted: make(chan struct{}, 2)}
	s := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{Fallback: &FillFirstSelector{}, TTL: time.Hour, RebindOn: SessionAffinityRebindOnQuota})
	m := NewManager(newMemoryAuthTestStore(), s, nil)
	m.SetRetryConfig(3, 5*time.Second, 5)
	m.RegisterExecutor(e)
	t.Cleanup(m.StopAutoRefresh)
	model := "rp-quota-" + strings.ReplaceAll(t.Name(), "/", "-")
	for _, item := range []struct{ suffix, priority string }{{"a", "10"}, {"b", "5"}} {
		id := model + "-" + item.suffix
		_, err := m.Register(context.Background(), &Auth{ID: id, Provider: "antigravity", Status: StatusActive, Attributes: map[string]string{"priority": item.priority}, Metadata: map[string]any{}})
		if err != nil {
			t.Fatal(err)
		}
		registry.GetGlobalRegistry().RegisterClient(id, "antigravity", []*registry.ModelInfo{{ID: model}, {ID: model + "-other"}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	}
	return m, e, model, model + "-a"
}

func rpQuotaOptions() coreexecutor.Options {
	return coreexecutor.Options{Headers: http.Header{"X-Rp-Client": {"openwebui"}, "X-Rp-User-Id": {"user-1"}, "X-Rp-Chat-Id": {"chat-1"}}, OriginalRequest: []byte(`{"messages":[{"role":"user","content":"same opening"}]}`)}
}

func runRPQuotaRequest(m *Manager, model, mode string) error {
	req, opts := coreexecutor.Request{Model: model}, rpQuotaOptions()
	if mode == "nonstream" {
		_, err := m.Execute(context.Background(), []string{"antigravity"}, req, opts)
		return err
	}
	result, err := m.ExecuteStream(context.Background(), []string{"antigravity"}, req, opts)
	if err != nil {
		return err
	}
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			err = chunk.Err
		}
	}
	return err
}

func TestPrivateRPQuotaReturns429ThenNextRequestAvoidsAccount(t *testing.T) {
	for _, mode := range []string{"nonstream", "http", "bootstrap", "midstream"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("CPA_PRIVATE_RP", "1")
			m, e, model, firstID := newRPQuotaManager(t, mode)
			if err := runRPQuotaRequest(m, model, mode); statusCodeFromError(err) != 429 {
				t.Fatalf("first error = %v, want 429", err)
			}
			e.mu.Lock()
			calls := len(e.calls)
			e.mu.Unlock()
			if calls != 1 {
				t.Fatalf("first request tried %d accounts, want exactly 1", calls)
			}
			<-e.probeStarted
			a, _ := m.GetByID(firstID)
			if !privateRPQuotaBlocked(a) {
				t.Fatal("account was not quarantined before 429 returned")
			}
			if _, exists := a.Metadata[rpQuotaSnapshotKey]; exists {
				t.Fatal("unknown remaining quota was fabricated")
			}
			// Different requested model, same chat: account quarantine remains global.
			if err := runRPQuotaRequest(m, model+"-other", "nonstream"); err != nil {
				t.Fatal(err)
			}
			e.mu.Lock()
			last := e.calls[len(e.calls)-1]
			e.mu.Unlock()
			if strings.HasPrefix(last, firstID+":") {
				t.Fatal("next request reused quarantined account")
			}
			for i := 0; i < 10; i++ {
				m.queuePrivateRPQuotaRefresh(context.Background(), a)
			}
			e.mu.Lock()
			probes := e.probeCalls
			e.mu.Unlock()
			if probes != 1 {
				t.Fatalf("concurrent quota probes = %d, want 1", probes)
			}
		})
	}
}

func TestPrivateRPQuotaModeOffPreservesUpstreamFailover(t *testing.T) {
	t.Setenv("CPA_PRIVATE_RP", "0")
	m, e, model, _ := newRPQuotaManager(t, "nonstream")
	if err := runRPQuotaRequest(m, model, "nonstream"); err != nil {
		t.Fatal(err)
	}
	if len(e.calls) != 2 {
		t.Fatalf("calls = %v, want upstream same-request failover", e.calls)
	}
}

func TestPrivateRPQuotaObservationFencesAndRecovery(t *testing.T) {
	t.Setenv("CPA_PRIVATE_RP", "1")
	m, _, model, id := newRPQuotaManager(t, "nonstream")
	if err := runRPQuotaRequest(m, model, "nonstream"); statusCodeFromError(err) != 429 {
		t.Fatal(err)
	}
	original, _ := m.GetByID(id)
	fence := original.Metadata[rpQuotaFenceKey].(string)
	if err := runRPQuotaRequest(m, model, "nonstream"); err != nil {
		t.Fatal(err)
	}
	// A separate model permission failure must survive quota restoration.
	m.MarkResult(context.Background(), Result{AuthID: id, Provider: "antigravity", Model: model + "-other", Error: &Error{HTTPStatus: 403, Message: "synthetic unrelated model permission failure"}})
	positive := 0.8
	snapshot := RPQuotaSnapshot{ObservedAt: time.Now(), Buckets: []RPQuotaBucket{{ID: "gemini-five-hour", RemainingFraction: &positive}}}
	m.applyPrivateRPQuotaSnapshot(original, fence, snapshot, nil)
	current, _ := m.GetByID(id)
	if !privateRPQuotaBlocked(current) {
		t.Fatal("lagging positive observation immediately cleared a 429")
	}
	if !strings.Contains(current.Metadata[rpQuotaSnapshotKey].(string), "0.8") {
		t.Fatal("real observed remaining value was not retained")
	}
	// Advance the account's due boundary explicitly; no wall-clock sleeping.
	m.mu.Lock()
	m.auths[id].Metadata[rpQuotaDueKey] = time.Now().Add(-time.Minute).Format(time.RFC3339Nano)
	m.auths[id].Quota.NextRecoverAt = time.Now().Add(-time.Minute)
	for _, state := range m.auths[id].ModelStates {
		state.NextRetryAfter = time.Now().Add(-time.Minute)
		state.Quota.NextRecoverAt = time.Now().Add(-time.Minute)
	}
	m.mu.Unlock()
	m.applyPrivateRPQuotaSnapshot(original, fence, RPQuotaSnapshot{}, errors.New("probe failed"))
	current, _ = m.GetByID(id)
	if !privateRPQuotaBlocked(current) {
		t.Fatal("failed query released quarantine")
	}
	m.mu.Lock()
	m.auths[id].Metadata[rpQuotaDueKey] = time.Now().Add(-time.Minute).Format(time.RFC3339Nano)
	m.mu.Unlock()
	m.applyPrivateRPQuotaSnapshot(original, "stale-fence", snapshot, nil)
	current, _ = m.GetByID(id)
	if !privateRPQuotaBlocked(current) {
		t.Fatal("stale response released quarantine")
	}
	m.applyPrivateRPQuotaSnapshot(original, fence, snapshot, nil)
	current, _ = m.GetByID(id)
	if privateRPQuotaBlocked(current) {
		t.Fatal("fresh positive revalidation did not release quarantine")
	}
	if blocked, _, _ := isAuthBlockedForModel(current, model, time.Now()); blocked {
		t.Fatal("revalidation left stale cooldown active")
	}
	if projection := m.clientModelProjectionForAuth(current, model, time.Now()); projection.Suspended || projection.QuotaExceeded {
		t.Fatal("model catalog still hides restored quota")
	}
	if blocked, _, _ := isAuthBlockedForModel(current, model+"-other", time.Now()); !blocked {
		t.Fatal("quota restoration erased unrelated model failure")
	}
	bound, _, _, err := m.pickNextMixed(context.Background(), []string{"antigravity"}, model, rpQuotaOptions(), nil)
	if err != nil || bound.ID == id {
		t.Fatal("restoration moved an already migrated chat back to A")
	}
	fresh := rpQuotaOptions()
	fresh.Headers.Set("X-RP-Chat-ID", "fresh-chat")
	bound, _, _, err = m.pickNextMixed(context.Background(), []string{"antigravity"}, model, fresh, nil)
	if err != nil || bound.ID != id {
		t.Fatalf("restored account not available to new chats: auth=%v err=%v", bound, err)
	}
}

func TestPrivateRPQuotaBoundaryProviderIsolation(t *testing.T) {
	t.Setenv("CPA_PRIVATE_RP", "1")
	err := customStatusError{code: 429, msg: "synthetic"}
	if IsPrivateRPQuotaBoundary([]string{"antigravity", "codex"}, err) {
		t.Fatal("unattributed mixed-provider 429 was classified as Antigravity")
	}
	if IsPrivateRPQuotaBoundary([]string{"codex"}, err) {
		t.Fatal("Codex behavior changed")
	}
	if !IsPrivateRPQuotaBoundary([]string{"antigravity"}, err) {
		t.Fatal("Antigravity 429 was not terminal")
	}
}

func TestPrivateRPQuotaSurvivesManagerReload(t *testing.T) {
	t.Setenv("CPA_PRIVATE_RP", "1")
	m, _, model, id := newRPQuotaManager(t, "nonstream")
	if err := runRPQuotaRequest(m, model, "nonstream"); statusCodeFromError(err) != 429 {
		t.Fatal(err)
	}
	reloaded := NewManager(m.store, &FillFirstSelector{}, nil)
	if err := reloaded.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	a, ok := reloaded.GetByID(id)
	if !ok || !privateRPQuotaBlocked(a) {
		t.Fatal("reload lost account quarantine")
	}
	if blocked, _, _ := isAuthBlockedForModel(a, model+"-other", time.Now().Add(24*time.Hour)); !blocked {
		t.Fatal("elapsed time alone released unverified quota")
	}
}
