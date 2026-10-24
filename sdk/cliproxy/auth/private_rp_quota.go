package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/rpmode"
)

const (
	rpQuotaBlockedKey  = "private_rp_quota_blocked"
	rpQuotaFenceKey    = "private_rp_quota_fence"
	rpQuotaDueKey      = "private_rp_quota_recheck_at"
	rpQuotaSnapshotKey = "private_rp_quota_snapshot"
	rpQuotaRetry       = time.Minute
)

// RPQuotaBucket is a baseline quota observation, not a token-to-subscription conversion.
type RPQuotaBucket struct {
	ID                string    `json:"bucket_id"`
	Window            string    `json:"window,omitempty"`
	RemainingFraction *float64  `json:"remaining_fraction,omitempty"`
	RemainingAmount   *int64    `json:"remaining_amount,omitempty"`
	Disabled          bool      `json:"disabled,omitempty"`
	ResetAt           time.Time `json:"reset_at,omitempty"`
}

// RPQuotaSnapshot contains the fields exposed by retrieveUserQuotaSummary.
type RPQuotaSnapshot struct {
	ObservedAt time.Time       `json:"observed_at"`
	Buckets    []RPQuotaBucket `json:"buckets"`
}

// RPQuotaRefresher is implemented by the Antigravity executor. It must not generate text.
type RPQuotaRefresher interface {
	RefreshRPQuota(context.Context, *Auth) (RPQuotaSnapshot, error)
}

type rpQuotaJob struct {
	cancel context.CancelFunc
}

type rpQuotaJobs struct {
	mu      sync.Mutex
	wg      sync.WaitGroup
	jobs    map[string]*rpQuotaJob
	slots   chan struct{}
	stopped bool
}

func privateRPAntigravity(auth *Auth) bool {
	return rpmode.Enabled() && auth != nil && strings.EqualFold(strings.TrimSpace(auth.Provider), "antigravity")
}

// IsPrivateRPQuotaBoundary prevents every retry layer from hiding a returned 429.
func IsPrivateRPQuotaBoundary(providers []string, err error) bool {
	if !rpmode.Enabled() || statusCodeFromError(err) != http.StatusTooManyRequests {
		return false
	}
	var marker interface{ IsPrivateRPQuotaBoundary() bool }
	if errors.As(err, &marker) && marker.IsPrivateRPQuotaBoundary() {
		return true
	}
	// Selection failures and SDK executors may not carry an upstream marker.
	// Do not attribute another provider's 429 in a mixed route to Antigravity.
	if len(providers) == 1 && strings.EqualFold(strings.TrimSpace(providers[0]), "antigravity") {
		return true
	}
	return false
}

func privateRPQuotaBlocked(auth *Auth) bool {
	if !privateRPAntigravity(auth) {
		return false
	}
	blocked, _ := auth.Metadata[rpQuotaBlockedKey].(bool)
	return blocked
}

func privateRPQuotaDue(auth *Auth) time.Time {
	raw, _ := auth.Metadata[rpQuotaDueKey].(string)
	due, _ := time.Parse(time.RFC3339Nano, raw)
	return due
}

// Called under the credential mutation lock and m.mu, before a 429 is returned.
func quarantinePrivateRPQuota(auth *Auth, result *Result, now time.Time) {
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	due := now.Add(rpQuotaRetry)
	if result.RetryAfter != nil && *result.RetryAfter > 0 {
		due = now.Add(*result.RetryAfter)
	}
	if previous := privateRPQuotaDue(auth); previous.After(due) {
		due = previous
	}
	auth.Metadata[rpQuotaBlockedKey] = true
	auth.Metadata[rpQuotaFenceKey] = uuid.NewString()
	auth.Metadata[rpQuotaDueKey] = due.UTC().Format(time.RFC3339Nano)
	// This is an intentional RP account quarantine, not a claim that every model
	// or quota bucket was exhausted. Preserve real remaining values separately.
	result.CredentialScope = true
	result.Error.Code = ErrorCodeForceCooldown
}

// Normal requests only scan local state. Network refreshes occur after a 429 or
// when a quarantined account is due for revalidation on a subsequent request.
func (m *Manager) refreshDuePrivateRPQuotas(ctx context.Context) {
	if m == nil || !rpmode.Enabled() || m.HomeEnabled() {
		return
	}
	now := time.Now()
	m.mu.RLock()
	var due []*Auth
	for _, auth := range m.auths {
		if privateRPQuotaBlocked(auth) && !auth.Disabled && !privateRPQuotaDue(auth).After(now) {
			due = append(due, auth.Clone())
		}
	}
	m.mu.RUnlock()
	for _, auth := range due {
		m.queuePrivateRPQuotaRefresh(ctx, auth)
	}
}

func (m *Manager) queuePrivateRPQuotaRefresh(ctx context.Context, auth *Auth) {
	if !privateRPQuotaBlocked(auth) {
		return
	}
	// Error-event publication may lazily populate fields on its own snapshot.
	// Detach synchronously before returning to that caller, not in the worker.
	auth = auth.Clone()
	m.mu.RLock()
	executor := m.executors["antigravity"]
	m.mu.RUnlock()
	refresher, ok := executor.(RPQuotaRefresher)
	if !ok {
		return
	}
	state := &m.rpQuotaJobs
	state.mu.Lock()
	if state.stopped || state.jobs[auth.ID] != nil {
		state.mu.Unlock()
		return
	}
	if state.slots == nil {
		state.slots = make(chan struct{}, 4)
	}
	if state.jobs == nil {
		state.jobs = make(map[string]*rpQuotaJob)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	jobCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	fence, _ := auth.Metadata[rpQuotaFenceKey].(string)
	job := &rpQuotaJob{cancel: cancel}
	state.jobs[auth.ID] = job
	state.wg.Add(1)
	state.mu.Unlock()
	go func() {
		defer state.wg.Done()
		acquired := false
		defer func() {
			cancel()
			state.mu.Lock()
			delete(state.jobs, auth.ID)
			if acquired {
				<-state.slots
			}
			state.mu.Unlock()
			// A second in-flight 429 may have replaced the observation fence.
			if latest, found := m.GetByID(auth.ID); found && privateRPQuotaBlocked(latest) && latest.Metadata[rpQuotaFenceKey] != fence {
				m.queuePrivateRPQuotaRefresh(context.Background(), latest)
			}
		}()
		select {
		case state.slots <- struct{}{}:
			acquired = true
		case <-jobCtx.Done():
			return
		}
		snapshot, err := refresher.RefreshRPQuota(jobCtx, auth.Clone())
		if jobCtx.Err() == nil {
			m.applyPrivateRPQuotaSnapshot(auth, fence, snapshot, err)
		}
	}()
}

func (m *Manager) applyPrivateRPQuotaSnapshot(original *Auth, fence string, snapshot RPQuotaSnapshot, probeErr error) {
	release := m.lockAuthMutation(original.ID)
	defer release()
	now := time.Now()
	m.mu.Lock()
	auth := m.auths[original.ID]
	if auth == nil || auth.RegistrationEpoch != original.RegistrationEpoch || auth.CredentialVersion != original.CredentialVersion || auth.Metadata[rpQuotaFenceKey] != fence || !privateRPQuotaBlocked(auth) {
		m.mu.Unlock()
		return
	}
	due := privateRPQuotaDue(auth)
	ready := probeErr == nil && !snapshot.ObservedAt.IsZero() && len(snapshot.Buckets) > 0
	next := now.Add(rpQuotaRetry)
	for _, bucket := range snapshot.Buckets {
		known := bucket.RemainingFraction != nil || bucket.RemainingAmount != nil
		positive := known && !bucket.Disabled && bucket.ID != ""
		if f := bucket.RemainingFraction; f != nil {
			positive = positive && *f > 0 && *f <= 1
		}
		if amount := bucket.RemainingAmount; amount != nil {
			positive = positive && *amount > 0
		}
		ready = ready && positive
		if !positive && bucket.ResetAt.After(next) {
			next = bucket.ResetAt
		}
	}
	if probeErr == nil && len(snapshot.Buckets) > 0 {
		if raw, err := json.Marshal(snapshot); err == nil {
			auth.Metadata[rpQuotaSnapshotKey] = string(raw)
		}
	}
	// Positive observations immediately following a 429 can lag enforcement.
	// Re-enable only after the retry boundary and a fresh successful check.
	if ready && !due.After(now) {
		auth.Metadata[rpQuotaBlockedKey] = false
		restorePrivateRPQuotaAvailability(auth, now)
	} else {
		if due.After(next) {
			next = due
		}
		auth.Metadata[rpQuotaDueKey] = next.UTC().Format(time.RFC3339Nano)
		if auth.Quota.Reason == "credential_quota" {
			auth.Quota.NextRecoverAt = next
			auth.NextRetryAfter = next
		}
	}
	auth.Generation++
	auth.UpdatedAt = now
	_ = m.persistLocked(context.Background(), auth)
	updated := auth.Clone()
	m.mu.Unlock()
	if m.scheduler != nil {
		m.scheduler.upsertAuth(updated)
	}
	reg := registry.GetGlobalRegistry()
	models, epoch := reg.GetModelsAndEpochForClient(updated.ID)
	projections := make([]registry.ClientModelProjection, 0, len(models))
	for _, model := range models {
		if model != nil {
			projections = append(projections, m.clientModelProjectionForAuth(updated, model.ID, now))
		}
	}
	reg.ApplyClientModelProjections(updated.ID, epoch, updated.Generation, projections)
	if !privateRPQuotaBlocked(updated) {
		m.persistCooldownStates(context.Background())
	}
}

func restorePrivateRPQuotaAvailability(auth *Auth, now time.Time) {
	// Only remove the credential-wide layer introduced by this quarantine.
	// Independent model/auth errors survive in the existing upstream shadow.
	for _, state := range auth.ModelStates {
		if state == nil || (!state.credentialQuotaPropagated && state.Quota.Reason != "credential_quota") {
			continue
		}
		if shadow := state.credentialQuotaShadow; shadow != nil && isModelStateActiveCooldown(shadow, now) {
			*state = *shadow.cloneShallow()
		} else {
			resetModelState(state, now)
		}
	}
	if auth.Quota.Reason == "credential_quota" {
		applyCooldownFields(&auth.Quota, QuotaState{})
	}
	if !hasUnauthorizedAuthFailure(auth) && !auth.Disabled && auth.Status != StatusDisabled {
		auth.Status = StatusActive
		auth.Unavailable = false
		auth.NextRetryAfter = time.Time{}
		if statusCodeFromResult(auth.LastError) == 429 {
			auth.LastError = nil
			auth.StatusMessage = ""
		}
		if len(auth.ModelStates) > 0 {
			updateAggregatedAvailability(auth, now)
		}
	}
}

func (m *Manager) stopPrivateRPQuotaRefreshes() {
	state := &m.rpQuotaJobs
	state.mu.Lock()
	state.stopped = true
	for _, job := range state.jobs {
		job.cancel()
	}
	state.mu.Unlock()
	state.wg.Wait()
}
